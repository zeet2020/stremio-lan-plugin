package main

import "testing"

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
