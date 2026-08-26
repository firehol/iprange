// WriterEdit import-surface arms (Rust writer_core/edit.rs
// begin_import_merge / map_import_feed / cached_import_membership /
// map_import_word_batch / finish_import_membership /
// release_import_cache / push_import_range / finish_import_merge): each
// arm binds one cache operation over the installed draft store, exactly
// like the Rust edit core.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// BeginImportMerge opens the import merge over the committed destination
// generation (Rust WriterEdit::begin_import_merge).
func (e *WriterEdit) BeginImportMerge(check func() error) (*ImportMerge, error) {
	return beginImportMerge(e.store, e.base, check)
}

// MapImportFeed records one source-to-destination feed mapping in the
// import cache (Rust WriterEdit::map_import_feed).
func (e *WriterEdit) MapImportFeed(cache *ImportCache, source, destination FeedEntry) error {
	return cache.mapFeed(e.store, source, destination)
}

// CachedImportMembership returns the stored translation of one source
// membership when it exists (Rust WriterEdit::cached_import_membership
// over Option<TranslatedMembership>); the caller forwards the pair to
// the import merge exactly like Rust push_import_range.
func (e *WriterEdit) CachedImportMembership(cache *ImportCache, source uint32) (id, words uint32, present bool, err error) {
	translated, present, err := cache.membership(e.store, source)
	if err != nil || !present {
		return 0, 0, present, err
	}
	return translated.id, translated.words, true, nil
}

// MapImportWordBatch maps one source bitmap word batch into the
// translation words through the feed map (Rust
// WriterEdit::map_import_word_batch); missing reports a source bitmap
// naming an inactive feed index.
func (e *WriterEdit) MapImportWordBatch(cache *ImportCache, words *ImportWords, start uint32, sourceWords []uint64, check func() error) (bool, error) {
	return cache.mapWordBatch(e.store, words, start, sourceWords, check)
}

// FinishImportMembership interns the translated words, records the
// translation, and returns the interned membership pair (Rust
// WriterEdit::finish_import_membership over
// Result<TranslatedMembership>).
func (e *WriterEdit) FinishImportMembership(cache *ImportCache, source uint32, words *ImportWords, check func() error) (id, wordCount uint32, err error) {
	interned, err := cache.finishMembership(e.store, source, words, check)
	if err != nil {
		return 0, 0, err
	}
	return interned.id, interned.words, nil
}

// ReleaseImportCache discards the cache tree (Rust
// WriterEdit::release_import_cache).
func (e *WriterEdit) ReleaseImportCache(cache *ImportCache, check func() error) error {
	return cache.release(e.store, check)
}

// PushImportRange streams one translated membership interval into the
// import merge (Rust WriterEdit::push_import_range over
// TranslatedMembership).
func (e *WriterEdit) PushImportRange(merge *ImportMerge, from, to tree.Key, id, words uint32, check func() error) error {
	return merge.push(e.store, from, to, translatedMembership{id: id, words: words}, check)
}

// FinishImportMerge completes the import merge and returns the exact
// before/after classification (Rust WriterEdit::finish_import_merge).
func (e *WriterEdit) FinishImportMerge(merge *ImportMerge, check func() error) (Comparison, error) {
	return merge.finish(e.store, check)
}
