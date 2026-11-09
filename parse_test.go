package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Parsed
	}{
		{"Inception.2010.1080p.BluRay.x264", Parsed{Title: "Inception", Year: 2010}},
		{"The Matrix (1999)", Parsed{Title: "The Matrix", Year: 1999}},
		{"Breaking.Bad.S01E02.720p.WEB-DL", Parsed{Title: "Breaking Bad", Season: 1, Episode: 2}},
		{"the office 2x05", Parsed{Title: "the office", Season: 2, Episode: 5}},
		{"Blade Runner 2049 (2017)", Parsed{Title: "Blade Runner 2049", Year: 2017}},
		{"2001 A Space Odyssey 1968", Parsed{Title: "2001 A Space Odyssey", Year: 1968}},
		{"Home Video", Parsed{Title: "Home Video"}},
		{"Some.Movie.1920x1080", Parsed{Title: "Some Movie 1920x1080"}},
		{"Show_Name - s03e10 - Title", Parsed{Title: "Show Name", Season: 3, Episode: 10}},
		{"[18VR.com]Sofi Goldfinger - DP Tissue Massage", Parsed{Title: "Sofi Goldfinger - DP Tissue Massage"}},
		{"[HorribleSubs] [Group] Show Name - S01E02 [1080p]", Parsed{Title: "Show Name", Season: 1, Episode: 2}},
		{"Movie [2019]", Parsed{Title: "Movie", Year: 2019}}, // only leading tags go
	}
	for _, c := range cases {
		if got := parse(c.in); got != c.want {
			t.Errorf("parse(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"The Matrix":           "matrix",
		"Amélie":               "amelie",
		"Fast & Furious":       "fastandfurious",
		"Theory of Everything": "theoryofeverything",
		"The":                  "the",
	} {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// Same file in a folder and its encoded/ subfolder: told apart by folder in
// the catalog name and in each stream.
func TestSameNameFolders(t *testing.T) {
	idx := newIndex()
	idx.add(&Item{Key: "a", Title: "Wedding", FileName: "Wedding.mp4", LocalFile: "/videos/source1/Wedding.mp4"})
	idx.add(&Item{Key: "b", Title: "Wedding", FileName: "Wedding.mp4", LocalFile: "/videos/source1/encoded/Wedding.mp4"})
	idx.add(&Item{Key: "c", Title: "Party", FileName: "Party.mp4", SMBShare: "Home", Path: "Party.mp4"})
	idx.add(&Item{Key: "d", Title: "Trip", FileName: "1.mkv", URL: "http://nas/MediaItems/1.mkv", Album: "Holidays"})
	idx.finish()

	for k, want := range map[string][2]string{
		"a": {"Wedding (source1)", "videos/source1"},
		"b": {"Wedding (encoded)", "source1/encoded"},
		"c": {"Party", "Home"},    // unique title: no suffix; SMB share root
		"d": {"Trip", "Holidays"}, // DLNA: its container
	} {
		it := idx.Items[k]
		if got := idx.movieName(it); got != want[0] {
			t.Errorf("%s name %q, want %q", k, got, want[0])
		}
		if got := it.folder(); got != want[1] {
			t.Errorf("%s folder %q, want %q", k, got, want[1])
		}
	}
	st := itemStream(httptest.NewRequest("GET", "/", nil), idx.Items["b"])
	if title := st["title"].(string); !strings.Contains(title, "📁 source1/encoded") {
		t.Errorf("stream title %q", title)
	}
}
