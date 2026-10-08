package search

import (
	"errors"
	"io/fs"
	"os"
	"runtime"
	"sync"

	"jrep/internal/chunk"
	"jrep/internal/walk"
)

// Load reads and chunks files in parallel, keeping file order. A file that
// vanished since it was listed is skipped.
func Load(files []walk.File, opts chunk.Options) ([]Chunk, error) {
	per := make([][]Chunk, len(files))
	errs := make([]error, len(files))
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), len(files)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				f := files[i]
				src, err := os.ReadFile(f.Path)
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				if err != nil {
					errs[i] = err
					continue
				}
				for _, c := range chunk.Split(f.Path, src, opts) {
					per[i] = append(per[i], Chunk{Path: f.Path, Rel: f.Rel, Chunk: c})
				}
			}
		}()
	}
	for i := range files {
		next <- i
	}
	close(next)
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	var out []Chunk
	for _, cs := range per {
		out = append(out, cs...)
	}
	return out, nil
}
