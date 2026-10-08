package acphost

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openCachedLog(t *testing.T, cache *ReadOnlyLogCache, path string) *Log {
	t.Helper()
	log, err := cache.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func writeCachedLogFixture(t testing.TB, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadOnlyLogCacheReadersCloseIndependently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.jsonl")
	writeCachedLogFixture(t, path, "{\"seq\":1,\"epoch\":2,\"turn\":3,\"kind\":\"turn_start\"}\n{\"seq\":2,\"epoch\":2,\"turn\":3,\"kind\":\"usage\"}\n")
	var cache ReadOnlyLogCache
	first := openCachedLog(t, &cache, path)
	second := openCachedLog(t, &cache, path)
	file := first.f
	if closeErr := first.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if _, statErr := file.Stat(); !errors.Is(statErr, os.ErrClosed) {
		t.Fatalf("closed reader retained a descriptor: %v", statErr)
	}
	page, err := second.History(math.MaxInt64, 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].Seq != 2 || page.OldestSeq != 1 || page.TruncatedBefore {
		t.Fatalf("independent page: %+v, %v", page, err)
	}
	if appendErr := second.Append(&Item{Kind: KindUsage}); !errors.Is(appendErr, ErrLogReadOnly) {
		t.Fatalf("cached reader is writable: %v", appendErr)
	}
}

func TestReadOnlyLogCacheSeesAppendReplacementAndDeletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.jsonl")
	writeCachedLogFixture(t, path, "{\"seq\":1,\"epoch\":2,\"turn\":3,\"kind\":\"turn_start\"}\n")
	var cache ReadOnlyLogCache
	first := openCachedLog(t, &cache, path)
	writer, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, writer, 1, KindUsage)
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	grown := openCachedLog(t, &cache, path)
	if grown.LastSeq() != 2 || grown.Len() != 2 || first.LastSeq() != 1 || first.Len() != 1 {
		t.Fatal("append changed the old snapshot or was missed by a new reader")
	}
	oldInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := path + ".replacement"
	contents := strings.ReplaceAll(readFile(t, path), `"epoch":2`, `"epoch":7`)
	writeCachedLogFixture(t, replacement, contents)
	if timesErr := os.Chtimes(replacement, oldInfo.ModTime(), oldInfo.ModTime()); timesErr != nil {
		t.Fatal(timesErr)
	}
	if closeErr := first.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if closeErr := grown.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if renameErr := os.Rename(replacement, path); renameErr != nil {
		t.Fatal(renameErr)
	}
	replaced := openCachedLog(t, &cache, path)
	last, _, active := replaced.state()
	if last.Epoch != 7 || last.Seq != 2 || !active {
		t.Fatalf("replacement inherited old identity: %+v active=%v", last, active)
	}
	if closeErr := replaced.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if removeErr := os.Remove(path); removeErr != nil {
		t.Fatal(removeErr)
	}
	if missing, openErr := cache.Open(path); !errors.Is(openErr, os.ErrNotExist) || missing != nil {
		t.Fatalf("deleted log came from cache: %v, %v", missing, openErr)
	}
	writeCachedLogFixture(t, path, "{\"seq\":1,\"epoch\":0,\"kind\":\"notice\"}\n")
	recreated := openCachedLog(t, &cache, path)
	last, _, active = recreated.state()
	if last.Seq != 1 || last.Epoch != 0 || active || recreated.Len() != 1 {
		t.Fatalf("recreated log inherited deleted state: %+v active=%v", last, active)
	}
}

func TestReadOnlyLogCacheRestoresLegacyCheckpointAfterReplacement(t *testing.T) {
	for _, suffix := range []string{"", "{\"seq\":41,\"epoch\":3,\"turn\":9,\"kind\":\"usage\"}\n"} {
		name := "checkpoint-only"
		if suffix != "" {
			name = "with-items"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "items.jsonl")
			const checkpoint = "{\"_acphost_checkpoint\":{\"last\":{\"seq\":40,\"epoch\":3,\"turn\":9,\"kind\":\"turn_start\"},\"open_turn\":true,\"mode\":\"ask\"}}\n"
			writeCachedLogFixture(t, path, checkpoint+suffix)
			var cache ReadOnlyLogCache
			first := openCachedLog(t, &cache, path)
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			// Legacy compaction replaced the file. Even with equal size and
			// mtime, the new checkpoint's identity and mode must be restored.
			changed := strings.ReplaceAll(checkpoint, `"seq":40`, `"seq":30`)
			changed = strings.ReplaceAll(changed, `"epoch":3`, `"epoch":7`)
			changed = strings.ReplaceAll(changed, `"turn":9`, `"turn":8`)
			changed = strings.ReplaceAll(changed, `"ask"`, `"new"`)
			replacement := path + ".replacement"
			writeCachedLogFixture(t, replacement, changed+suffix)
			if timesErr := os.Chtimes(replacement, before.ModTime(), before.ModTime()); timesErr != nil {
				t.Fatal(timesErr)
			}
			if closeErr := first.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if renameErr := os.Rename(replacement, path); renameErr != nil {
				t.Fatal(renameErr)
			}
			second := openCachedLog(t, &cache, path)
			if second.prefixSeq != 30 || second.Mode() != "new" || first.prefixSeq != 40 || first.Mode() != "ask" {
				t.Fatal("checkpoint state was reused across a file replacement")
			}
			if suffix == "" {
				last, _, active := second.state()
				if last.Seq != 30 || last.Epoch != 7 || last.Turn != 8 || !active {
					t.Fatalf("checkpoint-only identity: %+v active=%v", last, active)
				}
			}
			page, err := second.History(math.MaxInt64, 2)
			if err != nil || !page.TruncatedBefore {
				t.Fatalf("legacy prefix boundary: %+v, %v", page, err)
			}
			if closeErr := second.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			// A changed checkpoint must also go through normal validation.
			invalid := strings.ReplaceAll(changed, `"seq":30`, `"seq":-1`)
			writeCachedLogFixture(t, replacement, invalid+suffix)
			if timesErr := os.Chtimes(replacement, before.ModTime(), before.ModTime()); timesErr != nil {
				t.Fatal(timesErr)
			}
			if renameErr := os.Rename(replacement, path); renameErr != nil {
				t.Fatal(renameErr)
			}
			if log, openErr := cache.Open(path); openErr == nil || log != nil {
				t.Fatalf("invalid checkpoint bypassed validation: %v, %v", log, openErr)
			}
		})
	}
}

func TestReadOnlyLogCacheSeesTornTailCompletionAndModification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.jsonl")
	const complete = "{\"seq\":1,\"kind\":\"usage\"}\n"
	writeCachedLogFixture(t, path, strings.TrimSuffix(complete, "\n"))
	var cache ReadOnlyLogCache
	first := openCachedLog(t, &cache, path)
	if first.Len() != 0 {
		t.Fatal("torn record was indexed")
	}
	writeCachedLogFixture(t, path, complete)
	second := openCachedLog(t, &cache, path)
	if second.Len() != 1 || second.LastSeq() != 1 {
		t.Fatal("completed tail remained missing")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	writeCachedLogFixture(t, path, strings.ReplaceAll(complete, `"seq":1`, `"seq":2`))
	modified := info.ModTime().Add(time.Second)
	if timesErr := os.Chtimes(path, modified, modified); timesErr != nil {
		t.Fatal(timesErr)
	}
	third := openCachedLog(t, &cache, path)
	if third.LastSeq() != 2 {
		t.Fatal("same-size modified file reused an old index")
	}
	writeCachedLogFixture(t, path, "")
	if empty := openCachedLog(t, &cache, path); empty.Len() != 0 || empty.LastSeq() != 0 {
		t.Fatal("truncated file reused an old index")
	}
}

func TestReadOnlyLogCacheEvictionDoesNotRemoveHistory(t *testing.T) {
	root := t.TempDir()
	var cache ReadOnlyLogCache
	paths := make([]string, readOnlyLogCacheEntries+1)
	for i := range paths {
		paths[i] = filepath.Join(root, fmt.Sprintf("%d.jsonl", i))
		var contents strings.Builder
		for seq := range i + 1 {
			fmt.Fprintf(&contents, "{\"seq\":%d,\"kind\":\"usage\"}\n", seq+1)
		}
		writeCachedLogFixture(t, paths[i], contents.String())
		log := openCachedLog(t, &cache, paths[i])
		if closeErr := log.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	for i, path := range paths {
		log := openCachedLog(t, &cache, path)
		page, pageErr := log.History(math.MaxInt64, 1)
		if pageErr != nil || len(page.Items) != 1 || page.Items[0].Seq != int64(i+1) || page.OldestSeq != 1 || page.TruncatedBefore {
			t.Fatalf("cache eviction changed stored history %s: %+v, %v", path, page, pageErr)
		}
	}
}

func TestReadOnlyLogCacheConcurrentReadersPreservePages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.jsonl")
	writeCachedLogFixture(t, path, "{\"seq\":1,\"kind\":\"usage\"}\n{\"seq\":2,\"kind\":\"usage\"}\n")
	var cache ReadOnlyLogCache
	const readers = 16
	failures := make(chan error, readers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range readers {
		wg.Go(func() {
			<-start
			log, err := cache.Open(path)
			if err != nil {
				failures <- err
				return
			}
			defer func() { _ = log.Close() }()
			page, err := log.History(math.MaxInt64, 1)
			if err != nil || len(page.Items) != 1 || page.Items[0].Seq != 2 {
				failures <- fmt.Errorf("concurrent page: %+v, %v", page, err)
				return
			}
		})
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

func TestReadOnlyLogCacheSnapshotsWhileAppending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.jsonl")
	writer, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	appendN(t, writer, 1, KindUsage)
	var cache ReadOnlyLogCache
	const readers = 4
	failures := make(chan error, readers+1)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		<-start
		for range 40 {
			if appendErr := writer.Append(&Item{Kind: KindUsage}); appendErr != nil {
				failures <- appendErr
				return
			}
		}
	})
	for range readers {
		wg.Go(func() {
			<-start
			for range 20 {
				log, openErr := cache.Open(path)
				if openErr != nil {
					failures <- openErr
					return
				}
				page, readErr := log.History(math.MaxInt64, 2)
				last := log.LastSeq()
				closeErr := log.Close()
				if readErr != nil || closeErr != nil || len(page.Items) == 0 || page.Items[len(page.Items)-1].Seq != last || page.OldestSeq != 1 || page.TruncatedBefore {
					failures <- fmt.Errorf("append snapshot: page=%+v last=%d read=%v close=%v", page, last, readErr, closeErr)
					return
				}
			}
		})
	}
	close(start)
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
	latest := openCachedLog(t, &cache, path)
	if latest.LastSeq() != 41 || latest.Len() != 41 {
		t.Fatalf("completed appends missing: last=%d len=%d", latest.LastSeq(), latest.Len())
	}
}

func BenchmarkReadOnlyLogCacheHistory(b *testing.B) {
	for _, mib := range []int{8, 144} {
		b.Run(fmt.Sprintf("%dMiB", mib), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "items.jsonl")
			writer, err := OpenLog(path)
			if err != nil {
				b.Fatal(err)
			}
			payload := strings.Repeat("x", 64<<10)
			for range (mib << 20) / len(payload) {
				if appendErr := writer.Append(&Item{Kind: KindMessage, Message: &Message{Text: payload}}); appendErr != nil {
					b.Fatal(appendErr)
				}
			}
			if closeErr := writer.Close(); closeErr != nil {
				b.Fatal(closeErr)
			}
			var cache ReadOnlyLogCache
			warm, err := cache.Open(path)
			if err != nil {
				b.Fatal(err)
			}
			if closeErr := warm.Close(); closeErr != nil {
				b.Fatal(closeErr)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				log, openErr := cache.Open(path)
				if openErr != nil {
					b.Fatal(openErr)
				}
				page, readErr := log.History(math.MaxInt64, 2)
				closeErr := log.Close()
				if readErr != nil || closeErr != nil || len(page.Items) != 2 {
					b.Fatalf("page=%d read=%v close=%v", len(page.Items), readErr, closeErr)
				}
			}
		})
	}
}
