package ptyhost

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"github.com/3xDevOps/Aether/internal/domain"
)

type historyPageWindow struct {
	decoded      []historyDecodedLine
	decodedCount int
	newlineFree  bool
}

func decodeHistoryPageWindow(ctx context.Context, work *historyWorkDeadline, events []historyEvent, capacity int) (historyPageWindow, error) {
	ring := make([]historyDecodedLine, capacity)
	decodedCount, writeAt := 0, 0
	decoder := newHistoryDecoder(ctx, work, func(line historyDecodedLine) {
		ring[writeAt] = line
		writeAt = (writeAt + 1) % capacity
		decodedCount++
	})
	for i := len(events) - 1; i >= 0; i-- {
		if err := decoder.feed(events[i]); err != nil {
			return historyPageWindow{}, err
		}
	}
	completeLines := decodedCount
	if err := decoder.finish(); err != nil {
		return historyPageWindow{}, err
	}
	retained := decodedCount
	if retained > capacity {
		retained = capacity
	}
	decoded := make([]historyDecodedLine, 0, retained)
	start := 0
	if decodedCount >= capacity {
		start = writeAt
	}
	for i := 0; i < retained; i++ {
		decoded = append(decoded, ring[(start+i)%capacity])
	}
	unterminated := decodedCount > completeLines
	if unterminated && len(decoded) > 0 && decoded[len(decoded)-1].suffixText != "" {
		decoded[len(decoded)-1].text = decoded[len(decoded)-1].suffixText
	}
	newlineFree := completeLines == 0 && unterminated
	return historyPageWindow{
		decoded:      decoded,
		decodedCount: decodedCount,
		newlineFree:  newlineFree,
	}, nil
}

func buildHistoryPage(key []byte, run domain.RunID, limit int, window historyPageWindow, reachedOldest, discoveryStopped, workStopped bool, budget int, segmentIndex int, position int64, set string, segments []historySegment) HistoryPage {
	decoded := window.decoded
	validCount := window.decodedCount
	if !reachedOldest && validCount > 0 && !window.newlineFree {
		validCount--
		if window.decodedCount <= limit+1 && len(decoded) > 0 {
			decoded = decoded[1:]
		}
	}
	if len(decoded) > limit {
		decoded = decoded[len(decoded)-limit:]
	}
	budgetStopped := !reachedOldest && (budget <= 0 || discoveryStopped || workStopped)
	hasMore := validCount > limit || !reachedOldest || budgetStopped
	page := HistoryPage{Lines: make([]HistoryLine, 0, len(decoded)), HasMore: hasMore}
	for _, line := range decoded {
		page.Lines = append(page.Lines, HistoryLine{
			Cursor: encodeHistoryCursor(key, run, "", set, segments, line.origin),
			Time:   line.timeMS,
			Text:   line.text,
		})
	}
	if hasMore && len(page.Lines) > 0 {
		page.NextCursor = page.Lines[0].Cursor
	} else if hasMore {
		page.NextCursor = encodeHistoryCursor(key, run, "", set, segments, historyPosition{
			segment: segmentIndex, event: position, scan: true,
		})
	}
	return page
}

func historyDeadlinePage(key []byte, run domain.RunID, query, set string, segments []historySegment, cutoff historyPosition) HistoryPage {
	return HistoryPage{
		NextCursor: encodeHistoryCursor(key, run, query, set, segments, cutoff),
		HasMore:    true,
	}
}

func pageHistory(ctx context.Context, key []byte, run domain.RunID, limit int, cutoff historyPosition, set, path string, segments []historySegment, discovery *historySegmentDiscovery) (HistoryPage, error) {
	budget := maxHistoryPageRawRead
	work := newHistoryWorkDeadline(ctx)
	reader := historyReverseReader{ctx: ctx, work: work, budget: &budget, maxEvents: maxHistoryEvents}
	defer reader.close()
	deadlinePage := func() HistoryPage {
		return historyDeadlinePage(key, run, "", set, segments, cutoff)
	}
	stopped, err := work.check(ctx)
	if err != nil {
		return HistoryPage{}, err
	}
	if stopped {
		return deadlinePage(), nil
	}
	events := make([]historyEvent, 0, 128)
	reachedOldest := false
	discoveryStopped := false
	workStopped := false
	discovered := 0

	segmentIndex := cutoff.segment
	position := cutoff.event
	if cutoff.byte > 0 && !cutoff.scan {
		var event historyEvent
		var used int
		event, used, err = readHistoryEventAt(ctx, segments[segmentIndex], segmentIndex, cutoff.event)
		budget -= used
		if budget < 0 {
			budget = 0
		}
		if err != nil || cutoff.byte > len(event.data) {
			if err == nil {
				err = ErrInvalidHistoryCursor
			}
			return HistoryPage{}, err
		}
		event.data = event.data[:cutoff.byte]
		events = append(events, event)
		stopped, err = work.check(ctx)
		if err != nil {
			return HistoryPage{}, err
		}
		if stopped {
			return deadlinePage(), nil
		}
	}

	// Stop once the newest window can fill the page. A newline-free tail must
	// not drag the whole raw budget through the decoder: the page only keeps a
	// bounded suffix, and the resume cursor continues before that suffix.
	// A finished line does not count toward the byte cap. Otherwise one long
	// line would hide older lines, while a newline-free tail would still force
	// the decoder through the whole raw budget.
	neededNewlines := limit + 1
	outputBytes, newlines := 0, 0
	scanSegment := func() error {
		for {
			event, ok, readStopped, readErr := reader.previous(segments[segmentIndex], segmentIndex, &position)
			if readErr != nil {
				return readErr
			}
			if readStopped {
				workStopped = true
				return nil
			}
			if ok {
				events = append(events, event)
				newlines += bytes.Count(event.data, []byte{'\n'})
				if newlines == 0 {
					outputBytes += len(event.data)
					if outputBytes >= maxHistoryLineBytes {
						return nil
					}
				}
				if newlines >= neededNewlines {
					return nil
				}
			}
			if !ok && position == 0 {
				return nil
			}
		}
	}

	capacity := limit + 1
	if err = scanSegment(); err != nil {
		return HistoryPage{}, err
	}
	if work.elapsed {
		return deadlinePage(), nil
	}
	var window historyPageWindow
	for {
		window, err = decodeHistoryPageWindow(ctx, work, events, capacity)
		if errors.Is(err, errHistoryWorkDeadline) {
			return deadlinePage(), nil
		}
		if err != nil {
			return HistoryPage{}, err
		}
		// position > 0 means this segment still has older output. Return the
		// bounded window instead of skipping ahead to an older incarnation.
		if workStopped || window.decodedCount > limit || position > 0 {
			return buildHistoryPage(key, run, limit, window, false, false, workStopped, budget, segmentIndex, position, set, segments), nil
		}
		var next int
		var found bool
		next, found, stopped, err = moveToPreviousHistorySegment(ctx, work, path, &segments, segmentIndex, discovered, discovery)
		if err != nil {
			return HistoryPage{}, err
		}
		if work.elapsed {
			return deadlinePage(), nil
		}
		if stopped {
			discoveryStopped = true
			break
		}
		if !found {
			reachedOldest = true
			break
		}
		discovered++
		segmentIndex = next
		position = segments[segmentIndex].fileBytes
		if err = scanSegment(); err != nil {
			return HistoryPage{}, err
		}
		if work.elapsed {
			return deadlinePage(), nil
		}
	}
	window, err = decodeHistoryPageWindow(ctx, work, events, capacity)
	if errors.Is(err, errHistoryWorkDeadline) {
		return deadlinePage(), nil
	}
	if err != nil {
		return HistoryPage{}, err
	}
	return buildHistoryPage(key, run, limit, window, reachedOldest, discoveryStopped, workStopped, budget, segmentIndex, position, set, segments), nil
}

func searchHistory(ctx context.Context, key []byte, run domain.RunID, query string, limit int, cutoff historyPosition, set, path string, segments []historySegment, discovery *historySegmentDiscovery) (HistoryPage, error) {
	overlapBudget := maxHistoryEventBytes + 1
	budget := maxHistorySearchRawRead - overlapBudget
	work := newHistoryWorkDeadline(ctx)
	reader := historyReverseReader{ctx: ctx, work: work, budget: &budget, maxEvents: maxHistoryEvents}
	defer reader.close()
	deadlinePage := func() HistoryPage {
		return historyDeadlinePage(key, run, query, set, segments, cutoff)
	}
	stopped, err := work.check(ctx)
	if err != nil {
		return HistoryPage{}, err
	}
	if stopped {
		return deadlinePage(), nil
	}
	events := make([]historyEvent, 0, 128)
	decodedBytes := 0
	segmentIndex := cutoff.segment
	position := cutoff.event
	reachedOldest := false
	discoveryStopped := false
	workStopped := false
	discovered := 0

	if cutoff.byte > 0 && !cutoff.scan {
		event, used, err := readHistoryEventAt(ctx, segments[segmentIndex], segmentIndex, cutoff.event)
		budget -= used
		if budget < 0 {
			overlapBudget += budget
			budget = 0
		}
		if overlapBudget < 0 {
			overlapBudget = 0
		}
		if err != nil || cutoff.byte > len(event.data) {
			if err == nil {
				err = ErrInvalidHistoryCursor
			}
			return HistoryPage{}, err
		}
		event.data = event.data[:cutoff.byte]
		events = append(events, event)
		decodedBytes += len(event.data)
		stopped, err := work.check(ctx)
		if err != nil {
			return HistoryPage{}, err
		}
		if stopped {
			return deadlinePage(), nil
		}
	}

	for {
		beforeEvent := position
		event, ok, stopped, err := reader.previous(segments[segmentIndex], segmentIndex, &position)
		if err != nil {
			return HistoryPage{}, err
		}
		if stopped {
			workStopped = true
			break
		}
		if ok {
			if decodedBytes+len(event.data) > maxHistoryDecodeBytes {
				position = beforeEvent
				workStopped = true
				break
			}
			events = append(events, event)
			decodedBytes += len(event.data)
			continue
		}
		if position > 0 {
			continue
		}
		next, found, stopped, err := moveToPreviousHistorySegment(ctx, work, path, &segments, segmentIndex, discovered, discovery)
		if err != nil {
			return HistoryPage{}, err
		}
		if work.elapsed {
			return deadlinePage(), nil
		}
		if stopped {
			discoveryStopped = true
			break
		}
		if !found {
			reachedOldest = true
			break
		}
		discovered++
		segmentIndex = next
		position = segments[segmentIndex].fileBytes
	}
	if work.elapsed {
		return deadlinePage(), nil
	}

	boundary := historyPosition{segment: segmentIndex, event: position, scan: true}
	boundaryComplete := reachedOldest
	reader.budget = &overlapBudget
	for !boundaryComplete && !discoveryStopped {
		stopped, err := work.check(ctx)
		if err != nil {
			return HistoryPage{}, err
		}
		if stopped {
			return deadlinePage(), nil
		}
		if len(events) > 0 {
			oldest := &events[len(events)-1]
			if newline := bytes.LastIndexByte(oldest.data, '\n'); newline >= 0 {
				boundary = oldest.position
				boundary.byte = newline + 1
				oldest.position.byte = newline + 1
				oldest.data = oldest.data[newline+1:]
				boundaryComplete = true
				break
			}
		}
		beforeEvent := position
		event, ok, stopped, err := reader.previous(segments[segmentIndex], segmentIndex, &position)
		if err != nil {
			return HistoryPage{}, err
		}
		if stopped {
			workStopped = true
			break
		}
		if ok {
			if decodedBytes+len(event.data) > maxHistoryDecodeBytes {
				position = beforeEvent
				workStopped = true
				break
			}
			events = append(events, event)
			decodedBytes += len(event.data)
			continue
		}
		if position > 0 {
			continue
		}
		next, found, stopped, err := moveToPreviousHistorySegment(ctx, work, path, &segments, segmentIndex, discovered, discovery)
		if err != nil {
			return HistoryPage{}, err
		}
		if work.elapsed {
			return deadlinePage(), nil
		}
		if stopped {
			discoveryStopped = true
			break
		}
		if !found {
			reachedOldest = true
			boundaryComplete = true
			break
		}
		discovered++
		segmentIndex = next
		position = segments[segmentIndex].fileBytes
	}
	if work.elapsed {
		return deadlinePage(), nil
	}
	if !boundaryComplete {
		boundary = historyPosition{segment: segmentIndex, event: position, scan: true}
	}

	needle := foldHistoryString(query)
	matchRing := make([]historyDecodedLine, limit+1)
	matchCount, matchWrite := 0, 0
	decoder := newHistoryDecoder(ctx, work, func(line historyDecodedLine) {
		if !strings.Contains(foldHistoryString(line.text), needle) {
			return
		}
		matchRing[matchWrite] = line
		matchWrite = (matchWrite + 1) % len(matchRing)
		matchCount++
	})
	for i := len(events) - 1; i >= 0; i-- {
		if err := decoder.feed(events[i]); err != nil {
			if errors.Is(err, errHistoryWorkDeadline) {
				return deadlinePage(), nil
			}
			return HistoryPage{}, err
		}
	}
	if err := decoder.finish(); err != nil {
		if errors.Is(err, errHistoryWorkDeadline) {
			return deadlinePage(), nil
		}
		return HistoryPage{}, err
	}
	retained := matchCount
	if retained > len(matchRing) {
		retained = len(matchRing)
	}
	matches := make([]historyDecodedLine, 0, retained)
	start := 0
	if matchCount >= len(matchRing) {
		start = matchWrite
	}
	for i := 0; i < retained; i++ {
		matches = append(matches, matchRing[(start+i)%len(matchRing)])
	}
	returned := matches
	if matchCount > limit {
		returned = matches[len(matches)-limit:]
	}
	page := HistoryPage{
		Lines:   make([]HistoryLine, 0, len(returned)),
		HasMore: matchCount > limit || !reachedOldest || workStopped || discoveryStopped,
	}
	for _, line := range returned {
		page.Lines = append(page.Lines, HistoryLine{
			Cursor: encodeHistoryCursor(key, run, query, set, segments, line.origin),
			Time:   line.timeMS,
			Text:   line.text,
		})
	}
	if matchCount > limit {
		page.NextCursor = page.Lines[0].Cursor
	} else if page.HasMore {
		page.NextCursor = encodeHistoryCursor(key, run, query, set, segments, boundary)
	}
	return page, nil
}
