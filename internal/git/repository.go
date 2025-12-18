package git

import (
	"os"
)

type Repository struct {
	CloneDir string
}

func (r *Repository) Path() string {
	return r.CloneDir
}

func (r *Repository) Cleanup() error {
	return os.RemoveAll(r.CloneDir)
}
