// Package exporter turns a Confluence page tree into a single document.
// The pipeline is: BuildTree -> RenderHTML -> converter (PDF/DOCX).
package exporter

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence"
)

// ErrTooManyPages is returned when the tree exceeds the configured limit.
var ErrTooManyPages = errors.New("page tree exceeds the maximum number of pages")

// Source is the subset of the Confluence client needed to walk a tree.
type Source interface {
	GetPage(ctx context.Context, id string, withBody bool) (*confluence.Page, error)
	ListChildren(ctx context.Context, id string, withBody bool) ([]confluence.Page, error)
}

// Node is a page in the exported tree.
type Node struct {
	Page     confluence.Page
	Depth    int    // 0 for the root
	Number   string // hierarchical numbering, e.g. "1.2.3"
	Children []*Node
}

// Walk visits the tree depth-first, in document order.
func (n *Node) Walk(fn func(*Node)) {
	fn(n)
	for _, c := range n.Children {
		c.Walk(fn)
	}
}

// Count returns the number of pages in the tree.
func (n *Node) Count() int {
	total := 0
	n.Walk(func(*Node) { total++ })
	return total
}

type TreeOptions struct {
	IncludeChildren bool
	MaxPages        int
	Concurrency     int
	// OnProgress is called (concurrently) with the number of pages fetched so far.
	OnProgress func(fetched int)
}

// BuildTree fetches the root page and, optionally, all its descendants with
// their bodies. Sibling order is preserved; children listings run
// concurrently with at most opts.Concurrency requests in flight.
func BuildTree(ctx context.Context, src Source, rootID string, opts TreeOptions) (*Node, error) {
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}
	rootPage, err := src.GetPage(ctx, rootID, true)
	if err != nil {
		return nil, err
	}
	root := &Node{Page: *rootPage}
	w := &walker{src: src, opts: opts, sem: make(chan struct{}, opts.Concurrency), seen: map[string]bool{rootID: true}}
	w.fetched.Store(1)
	w.progress()

	if opts.IncludeChildren {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		w.cancel = cancel
		w.wg.Add(1)
		go w.visit(ctx, root)
		w.wg.Wait()
		if w.err != nil {
			return nil, w.err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	number(root, "1")
	return root, nil
}

type walker struct {
	src     Source
	opts    TreeOptions
	sem     chan struct{}
	wg      sync.WaitGroup
	fetched atomic.Int64
	cancel  context.CancelFunc

	mu   sync.Mutex
	err  error
	seen map[string]bool
}

func (w *walker) fail(err error) {
	w.mu.Lock()
	if w.err == nil {
		w.err = err
	}
	w.mu.Unlock()
	w.cancel()
}

// visit lists n's children then recurses. The semaphore only guards the
// HTTP call, never the spawning of goroutines, so it cannot deadlock.
func (w *walker) visit(ctx context.Context, n *Node) {
	defer w.wg.Done()
	select {
	case w.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	pages, err := w.src.ListChildren(ctx, n.Page.ID, true)
	<-w.sem
	if err != nil {
		w.fail(fmt.Errorf("listing children of page %s: %w", n.Page.ID, err))
		return
	}

	w.mu.Lock()
	children := make([]*Node, 0, len(pages))
	for _, p := range pages {
		if w.seen[p.ID] { // defensive: Confluence trees have no cycles
			continue
		}
		w.seen[p.ID] = true
		children = append(children, &Node{Page: p, Depth: n.Depth + 1})
	}
	n.Children = children
	w.mu.Unlock()

	if total := w.fetched.Add(int64(len(children))); int(total) > w.opts.MaxPages {
		w.fail(fmt.Errorf("%w (limit %d)", ErrTooManyPages, w.opts.MaxPages))
		return
	}
	w.progress()
	for _, c := range children {
		w.wg.Add(1)
		go w.visit(ctx, c)
	}
}

func (w *walker) progress() {
	if w.opts.OnProgress != nil {
		w.opts.OnProgress(int(w.fetched.Load()))
	}
}

func number(n *Node, prefix string) {
	n.Number = prefix
	for i, c := range n.Children {
		number(c, prefix+"."+strconv.Itoa(i+1))
	}
}
