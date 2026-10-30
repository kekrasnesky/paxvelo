package main

import (
	"strings"
	"testing"
)

func TestGroupRoutes(t *testing.T) {
	r := func(id, name string, miles, feet float64, rides int, usual string) Route {
		return Route{ID: id, Name: name, Distance: miles * 1609.344, Elevation: feet / 3.28084, Rides: rides, Usual: usual}
	}
	cards := groupRoutes([]Route{
		r("1", "PV ThNR", 39, 580, 23, ""),
		r("2", "ThNR Point Lookout", 41, 620, 12, ""),
		r("3", "Base Laps", 34, 490, 29, ""),
		r("4", "Base Laps - 3X", 33, 470, 3, "TNR B Grp"),
		r("5", "Off-Base Laps (short)", 26, 550, 1, ""),
		r("6", "SuMR - Steer Horn", 38, 1250, 17, "SuMR"),
		r("7", "Steerhorn Neck", 40, 1220, 6, ""),
		r("8", "SuMR - Coltons Pt", 41, 1190, 18, "SuMR"),
		r("9", "Colton’s Point from Leonardtown McDonalds", 40, 1040, 3, ""),
		r("10", "Old Line to Colton’s Point", 58, 1600, 3, ""),      // much longer: its own ride
		r("11", "SuMR - Donut Ride 2 Piney Point", 42, 1040, 6, ""), // shares only "point" with Colton's
		r("12", "Piney Point TT", 16, 360, 5, ""),                   // same place, much shorter
		r("13", "Friday Jellyfish", 20, 660, 1, ""),                 // ridden once: left off
	})
	var got []string
	for _, c := range cards {
		var vs []string
		for _, v := range c.Variants {
			vs = append(vs, v.ID)
		}
		got = append(got, c.Name+"="+itoa(c.Rides)+"["+strings.Join(vs, ",")+"]")
	}
	want := []string{
		"Tuesday Night Ride=33[4,5]", "Thursday Night Ride=35[2]",
		"SuMR - Steer Horn=23[7]", "SuMR - Coltons Pt=21[9]",
		"SuMR - Donut Ride 2 Piney Point=6[]", "Piney Point TT=5[]", "Old Line to Colton’s Point=3[]",
	}
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Errorf("cards:\n %s\nwant\n %s", strings.Join(got, "\n "), strings.Join(want, "\n "))
	}
	if cards[0].ID != "3" || cards[0].Usual != "" {
		t.Errorf("the Tuesday card should show its most ridden route: %+v", cards[0].Route)
	}
}
