package oauth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

type CallbackResult struct {
	Code  string
	State string
	Err   string
}

func (m *Manager) Login(ctx context.Context, provider config.OAuthProvider, openBrowser bool) (string, error) {
	if !provider.IsValid() {
		return "", fmt.Errorf("unsupported oauth provider %q", provider)
	}
	pkce, err := GeneratePKCE()
	if err != nil {
		return "", err
	}
	state, err := RandomURLSafe(18)
	if err != nil {
		return "", err
	}
	nonce, err := RandomURLSafe(18)
	if err != nil {
		return "", err
	}

	addr := m.CallbackAddr(provider)
	path := "/auth/callback"
	redirectURI := codexRedirectURI(addr)
	if provider == ProviderXAI {
		path = "/callback"
		redirectURI = xaiRedirectURI(addr)
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("start oauth callback listener on %s: %w", addr, err)
	}
	resultCh := make(chan CallbackResult, 1)
	server := &http.Server{
		Handler:           callbackHandler(path, state, resultCh),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			select {
			case resultCh <- CallbackResult{Err: err.Error()}:
			default:
			}
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	authURL, err := BuildAuthURL(provider, redirectURI, state, nonce, pkce)
	if err != nil {
		return "", err
	}
	fmt.Printf("Open this URL to authenticate %s:\n%s\n", provider, authURL)
	if openBrowser {
		cmdName, args := browserCommand(authURL)
		if err := exec.Command(cmdName, args...).Start(); err != nil {
			fmt.Printf("Could not open browser automatically: %v\n", err)
		}
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-resultCh:
		if strings.TrimSpace(result.Err) != "" {
			return "", fmt.Errorf("oauth callback error: %s", result.Err)
		}
		if result.State != state {
			return "", fmt.Errorf("oauth callback state mismatch")
		}
		token, err := m.ExchangeCode(ctx, provider, result.Code, redirectURI, pkce)
		if err != nil {
			return "", err
		}
		path, err := m.SaveToken(token)
		if err != nil {
			return "", err
		}
		return path, nil
	}
}

func callbackHandler(path, wantState string, out chan<- CallbackResult) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		state := q.Get("state")
		code := q.Get("code")
		errMsg := firstNonEmpty(q.Get("error_description"), q.Get("error"))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if state != wantState {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("<html><body><h1>Authentication failed</h1><p>You can close this window.</p></body></html>"))
			return
		}
		if errMsg != "" {
			select {
			case out <- CallbackResult{State: state, Err: errMsg}:
			default:
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("<html><body><h1>Authentication failed</h1><p>You can close this window.</p></body></html>"))
			return
		}
		if code == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("<html><body><h1>Authentication failed</h1><p>You can close this window.</p></body></html>"))
			return
		}
		result := CallbackResult{
			Code:  code,
			State: state,
		}
		select {
		case out <- result:
		default:
		}
		_, _ = w.Write([]byte("<html><body><h1>Authentication complete</h1><p>You can close this window and return to droid-proxy.</p></body></html>"))
	})
	return mux
}
