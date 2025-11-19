package git

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

const (
	CloneBaseDir = "/git-repos"
)

type Repository struct {
	URL       string
	Reference string
}

func NewRepository(ctx context.Context, repoUrl, reference string) (*Repository, error) {
	_, err := url.Parse(repoUrl)
	if err != nil {
		return nil, fmt.Errorf("invalid repository URL: %w", err)
	}

	repo := &Repository{
		URL:       repoUrl,
		Reference: reference,
	}

	exists, err := repo.alreadyCloned()
	if err != nil {
		return nil, fmt.Errorf("failed to check if repository already cloned: %w", err)
	}

	if !exists {
		err := repo.clone(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to clone repository: %w", err)
		}
	}

	return repo, nil
}

func (r *Repository) Path() string {
	u, _ := url.Parse(r.URL) // we parsed it already initially
	path := fmt.Sprintf("%s/%s/%s/%s", CloneBaseDir, u.Host, strings.TrimSuffix(u.Path, ".git"), r.Reference)

	return path
}

func (r *Repository) Cleanup() error {
	return os.RemoveAll(r.Path())
}

func (r *Repository) alreadyCloned() (bool, error) {
	info, err := os.Stat(r.Path())
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	// Path exists, return true if it's a directory
	return info.IsDir(), nil
}

func (r *Repository) clone(ctx context.Context) error {
	// ensure path exists
	err := os.MkdirAll(r.Path(), 0755)
	if err != nil {
		return fmt.Errorf("failed to create repository directory: %w", err)
	}

	_, err = git.PlainCloneContext(ctx, r.Path(), &git.CloneOptions{
		URL:           r.URL,
		ReferenceName: plumbing.ReferenceName(r.Reference),
		SingleBranch:  true,
		Depth:         1,
	})
	if err != nil {
		return fmt.Errorf("failed to clone repo: %w", err)
	}

	return nil
}
