// internal/drive/drive.go

// Package drive wraps the Google Drive API v3 behind a narrow Client
// interface so billsort's orchestration logic can be tested with a fake.
// It knows how to walk (and create) the /<año>/<casa>/<categoría>/ folder
// tree, list files in a folder, upload files under their computed names,
// and download files back down for re-reading. It authenticates via an
// OAuth2 "installed app" flow using only the drive.file scope (minimal
// access, limited to files this app creates — see spec).
package drive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	gdrive "google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// folderMIME is Drive's content type for folders, used both to query for
// them and to create them.
const folderMIME = "application/vnd.google-apps.folder"

// FileEntry is one file's name and Drive file ID, as returned by ListFiles.
type FileEntry struct {
	Name string
	ID   string
}

// Client is the seam between billsort's orchestration logic and the real
// Google Drive API — lets main.go's flow be tested with a fake.
type Client interface {
	// ListFiles returns every file currently in the folder identified by
	// pathSegments (e.g. []string{"2026", "Igarzabal", "Agua"}). Returns an
	// empty slice, not an error, if the folder doesn't exist yet.
	ListFiles(ctx context.Context, pathSegments []string) ([]FileEntry, error)

	// EnsureFolder creates any missing segment of pathSegments and returns the
	// leaf folder's Drive file ID.
	EnsureFolder(ctx context.Context, pathSegments []string) (folderID string, err error)

	// Upload uploads the contents of localPath into the folder identified by
	// folderID, naming it filename.
	Upload(ctx context.Context, folderID, filename, localPath string) error

	// Download saves the Drive file identified by fileID to destLocalPath.
	// Used to re-read a pending factura's content since Drive only stores
	// filenames, not the amount billsort needs for pago matching.
	Download(ctx context.Context, fileID, destLocalPath string) error
}

// ParseNN extracts the leading "NN-" load-counter from a billsort-convention
// filename. ok is false for filenames that don't start with digits followed
// by a hyphen (e.g. the untouched Rentas/Fiat Toro filenames this tool never
// generates or reads as NN-bearing).
func ParseNN(filename string) (int, bool) {
	i := strings.IndexByte(filename, '-')
	if i <= 0 {
		return 0, false
	}
	n, err := strconv.Atoi(filename[:i])
	if err != nil {
		return 0, false
	}
	return n, true
}

// driveClient is the real Client implementation backed by the Drive API v3.
type driveClient struct {
	svc *gdrive.Service
}

// NewClient builds a Drive client using an OAuth2 "installed app" flow:
// reads the OAuth client secret at credentialsPath, reuses a cached token
// from tokenCachePath if present and valid, otherwise opens a browser for
// one-time user consent and writes the resulting token to tokenCachePath.
func NewClient(ctx context.Context, credentialsPath, tokenCachePath string) (Client, error) {
	data, err := os.ReadFile(credentialsPath)
	if err != nil {
		return nil, fmt.Errorf("reading OAuth credentials %q: %w", credentialsPath, err)
	}
	conf, err := google.ConfigFromJSON(data, gdrive.DriveFileScope)
	if err != nil {
		return nil, fmt.Errorf("parsing OAuth credentials %q: %w", credentialsPath, err)
	}

	tok := loadToken(tokenCachePath)
	if tok == nil {
		tok, err = tokenFromWeb(ctx, conf)
		if err != nil {
			return nil, fmt.Errorf("getting OAuth token: %w", err)
		}
		if err := saveToken(tokenCachePath, tok); err != nil {
			return nil, fmt.Errorf("caching OAuth token: %w", err)
		}
	}

	svc, err := gdrive.NewService(ctx, option.WithHTTPClient(conf.Client(ctx, tok)))
	if err != nil {
		return nil, fmt.Errorf("creating Drive service: %w", err)
	}
	return &driveClient{svc: svc}, nil
}

// ListFiles returns the non-folder children of the folder at pathSegments,
// or an empty slice if any segment of the path doesn't exist yet.
func (c *driveClient) ListFiles(ctx context.Context, pathSegments []string) ([]FileEntry, error) {
	folderID, err := c.resolveFolder(ctx, pathSegments, false)
	if err != nil {
		return nil, fmt.Errorf("resolving %q: %w", strings.Join(pathSegments, "/"), err)
	}
	if folderID == "" {
		return []FileEntry{}, nil
	}

	q := fmt.Sprintf("'%s' in parents and mimeType != '%s' and trashed = false", quoteQ(folderID), folderMIME)
	out := make([]FileEntry, 0)
	err = c.svc.Files.List().Q(q).Fields("nextPageToken, files(id, name)").Pages(ctx, func(fl *gdrive.FileList) error {
		for _, f := range fl.Files {
			out = append(out, FileEntry{Name: f.Name, ID: f.Id})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing files in %q: %w", strings.Join(pathSegments, "/"), err)
	}
	return out, nil
}

// EnsureFolder walks pathSegments creating any missing segment as a Drive
// folder, then returns the leaf folder's ID.
func (c *driveClient) EnsureFolder(ctx context.Context, pathSegments []string) (string, error) {
	folderID, err := c.resolveFolder(ctx, pathSegments, true)
	if err != nil {
		return "", fmt.Errorf("ensuring %q: %w", strings.Join(pathSegments, "/"), err)
	}
	return folderID, nil
}

// Upload uploads the contents of localPath into folderID under the given
// filename (multipart media upload; no format conversion — the extension is
// preserved exactly as computed by the caller).
func (c *driveClient) Upload(ctx context.Context, folderID, filename, localPath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("opening %q: %w", localPath, err)
	}
	defer f.Close()

	_, err = c.svc.Files.Create(&gdrive.File{
		Name:    filename,
		Parents: []string{folderID},
	}).Media(f).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("uploading %q as %q: %w", localPath, filename, err)
	}
	return nil
}

// Download saves the Drive file fileID to destLocalPath. Download() guarantees
// a 2xx response when it returns a nil error, so the body can be copied
// straight to disk.
func (c *driveClient) Download(ctx context.Context, fileID, destLocalPath string) error {
	resp, err := c.svc.Files.Get(fileID).Context(ctx).Download()
	if err != nil {
		return fmt.Errorf("downloading file %q: %w", fileID, err)
	}
	defer resp.Body.Close()

	out, err := os.Create(destLocalPath)
	if err != nil {
		return fmt.Errorf("creating %q: %w", destLocalPath, err)
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		return fmt.Errorf("writing %q: %w", destLocalPath, err)
	}
	return out.Close()
}

// resolveFolder walks pathSegments one segment at a time, starting from My
// Drive root, looking up each child folder by name. With create=false it
// returns "" as soon as a segment is missing; with create=true it creates
// missing segments instead.
func (c *driveClient) resolveFolder(ctx context.Context, pathSegments []string, create bool) (string, error) {
	parent := "root"
	for _, seg := range pathSegments {
		q := fmt.Sprintf("'%s' in parents and mimeType = '%s' and name = '%s' and trashed = false",
			quoteQ(parent), folderMIME, quoteQ(seg))
		lst, err := c.svc.Files.List().Q(q).PageSize(2).Fields("files(id)").Context(ctx).Do()
		if err != nil {
			return "", fmt.Errorf("looking up folder %q: %w", seg, err)
		}
		if len(lst.Files) > 0 {
			parent = lst.Files[0].Id
			continue
		}
		if !create {
			return "", nil
		}
		f, err := c.svc.Files.Create(&gdrive.File{
			Name:     seg,
			MimeType: folderMIME,
			Parents:  []string{parent},
		}).Context(ctx).Do()
		if err != nil {
			return "", fmt.Errorf("creating folder %q in %q: %w", seg, parent, err)
		}
		parent = f.Id
	}
	return parent, nil
}

// quoteQ escapes a value for safe interpolation into a Drive Files.List q=
// query string (single quotes are the string delimiter there).
func quoteQ(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `'`, `\'`)
}

// loadToken reads a cached oauth2.Token from path, returning nil if the file
// doesn't exist or can't be parsed (in which case we fall back to the web
// flow). An expired-but-refreshable cached token is fine to return: the
// oauth2 config refreshes it automatically on first use.
func loadToken(path string) *oauth2.Token {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	tok := new(oauth2.Token)
	if err := json.Unmarshal(data, tok); err != nil {
		return nil
	}
	return tok
}

// saveToken writes tok to path as JSON so subsequent runs skip the consent
// flow. The file holds a bearer credential, so it's written owner-only.
func saveToken(path string, tok *oauth2.Token) error {
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// tokenFromWeb runs the OAuth2 loopback-IP flow for installed apps: it binds
// an ephemeral localhost port, sends the user to Google's consent page, and
// captures the authorization code that Google redirects back to us.
func tokenFromWeb(ctx context.Context, conf *oauth2.Config) (*oauth2.Token, error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listening on loopback: %w", err)
	}
	defer lis.Close()
	conf.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d", lis.Addr().(*net.TCPAddr).Port)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("code") != "":
			fmt.Fprintln(w, "billsort autorizado. Podés cerrar esta ventana.")
			codeCh <- q.Get("code")
		case q.Get("error") != "":
			// Standard OAuth way of signaling denial/failure via redirect.
			http.Error(w, "billsort no fue autorizado.", http.StatusBadRequest)
			desc := q.Get("error_description")
			if desc != "" {
				desc = ": " + desc
			}
			errCh <- fmt.Errorf("autorización denegada (%s%s)", q.Get("error"), desc)
		default:
			// Stray request with neither code nor error (e.g. the automatic
			// GET /favicon.ico browsers issue when loading any page):
			// ignore it, don't touch either channel.
			http.NotFound(w, r)
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(lis)
	defer srv.Close()

	url := conf.AuthCodeURL("state")
	fmt.Printf("Abrí esta URL en tu navegador para autorizar billsort:\n\n%s\n\nEsperando autorización...\n", url)
	tryOpenBrowser(url)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-errCh:
		return nil, err
	case code := <-codeCh:
		return conf.Exchange(ctx, code)
	}
}

// tryOpenBrowser attempts to open url in the system browser, ignoring
// failures (the URL was printed to stdout anyway).
func tryOpenBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
