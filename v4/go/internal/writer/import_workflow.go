// WriterEdit import-surface arms (Rust writer_core/edit.rs
// begin_import_merge / map_import_feed / cached_import_membership /
// map_import_word_batch / finish_import_membership /
// release_import_cache / push_import_range / finish_import_merge): each
// arm binds one cache operation over the installed draft store, exactly
// like the Rust edit core.

package writer

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

// PushImportRange4 streams one translated IPv4 membership interval
// into the import merge (Rust WriterEdit::push_import_range over
// TranslatedMembership and Ipv4Key).
func (e *WriterEdit) PushImportRange4(merge *ImportMerge, from, to uint32, id, words uint32, check func() error) error {
	return merge.push4(e.store, key4(from), key4(to), translatedMembership{id: id, words: words}, check)
}

// PushImportRange6 is the IPv6 form of PushImportRange4.
func (e *WriterEdit) PushImportRange6(merge *ImportMerge, fromHi, fromLo, toHi, toLo uint64, id, words uint32, check func() error) error {
	return merge.push6(e.store, key6{Hi: fromHi, Lo: fromLo}, key6{Hi: toHi, Lo: toLo}, translatedMembership{id: id, words: words}, check)
}

// FinishImportMerge4 completes the IPv4 import merge and returns the
// exact before/after classification (Rust
// WriterEdit::finish_import_merge over Ipv4Key).
func (e *WriterEdit) FinishImportMerge4(merge *ImportMerge, check func() error) (Comparison, error) {
	return merge.finish4(e.store, check)
}

// FinishImportMerge6 is the IPv6 form of FinishImportMerge4.
func (e *WriterEdit) FinishImportMerge6(merge *ImportMerge, check func() error) (Comparison, error) {
	return merge.finish6(e.store, check)
}
