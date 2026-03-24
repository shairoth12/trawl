package chain

import (
	"context"
	"net/http"
)

// Fetcher abstracts the remote fetch operation.
type Fetcher interface {
	Fetch(ctx context.Context, url string) (*http.Response, error)
}

// Repository layer — makes the actual external call.
type Repository struct{}

func (r *Repository) Fetch(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}

// Service layer — delegates to repository.
type Service struct {
	repo Fetcher
}

func NewService(repo Fetcher) *Service {
	return &Service{repo: repo}
}

func (s *Service) FetchData(ctx context.Context, url string) (*http.Response, error) {
	return s.repo.Fetch(ctx, url)
}

// HandleChain is the handler layer entry point — calls service.
func HandleChain(w http.ResponseWriter, r *http.Request) {
	svc := NewService(&Repository{})
	resp, err := svc.FetchData(r.Context(), "https://example.com/data")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	w.WriteHeader(resp.StatusCode)
}
