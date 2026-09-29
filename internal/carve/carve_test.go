package carve_test

import (
	"bytes"
	"context"
	"math/rand"
	"testing"

	"github.com/shanurwan/pagerelic/internal/carve"
	"github.com/shanurwan/pagerelic/internal/fixture"
	"github.com/shanurwan/pagerelic/internal/page"
)

// TestCarveFromRawMedium scatters the real pages of two tables across a
// synthetic disk (shuffled, 4 KiB aligned, random filler between) and
// requires every page back, correctly grouped, with its true block number
// among the checksum-derived candidates.
func TestCarveFromRawMedium(t *testing.T) {
	people := fixture.Bytes(t, "raw/base/16388/16434")
	accounts := fixture.Bytes(t, "stages/A-intact/accounts.heap")
	type planted struct {
		group string
		blk   uint32
		data  []byte
	}
	var pages []planted
	for blk := 0; blk*page.DefaultSize < len(people); blk++ {
		pages = append(pages, planted{"heap-natts20", uint32(blk), people[blk*page.DefaultSize : (blk+1)*page.DefaultSize]})
	}
	for blk := 0; blk*page.DefaultSize < len(accounts); blk++ {
		pages = append(pages, planted{"heap-natts5", uint32(blk), accounts[blk*page.DefaultSize : (blk+1)*page.DefaultSize]})
	}
	rng := rand.New(rand.NewSource(3))
	rng.Shuffle(len(pages), func(i, j int) { pages[i], pages[j] = pages[j], pages[i] })

	var disk []byte
	where := map[int64]planted{}
	for _, p := range pages {
		gap := make([]byte, 4096*(1+rng.Intn(3)))
		rng.Read(gap)
		disk = append(disk, gap...)
		where[int64(len(disk))] = p
		disk = append(disk, p.data...)
	}
	disk = append(disk, make([]byte, 4096)...)

	found := 0
	st, err := carve.Scan(context.Background(), bytes.NewReader(disk), int64(len(disk)), carve.Options{},
		func(p carve.Page, b []byte) error {
			want, ok := where[p.Offset]
			if !ok {
				t.Fatalf("false positive page at offset %d (%s)", p.Offset, p.Group)
			}
			if p.Group != want.group {
				t.Fatalf("page at %d grouped %s, want %s", p.Offset, p.Group, want.group)
			}
			hit := false
			for _, c := range p.Candidates {
				hit = hit || c == want.blk
			}
			if !hit {
				t.Fatalf("block %d not among candidates %v", want.blk, p.Candidates)
			}
			found++
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if found != len(pages) || st.Pages != len(pages) {
		t.Fatalf("carved %d of %d pages", found, len(pages))
	}
}

func FuzzExamine(f *testing.F) {
	f.Add(fixture.Bytes(f, "raw/base/16388/16434")[:page.DefaultSize])
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) < page.DefaultSize {
			return
		}
		carve.Scan(context.Background(), bytes.NewReader(b), int64(len(b)), carve.Options{MaxBlock: 64}, func(carve.Page, []byte) error { return nil })
	})
}
