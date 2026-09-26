package plugin

import (
	"reflect"
	"testing"

	"github.com/spf13/cobra"
)

func TestBackgroundArgs(t *testing.T) {
	root := &cobra.Command{Use: "sessh"}
	root.PersistentFlags().String("profile", "", "")
	capture := &cobra.Command{Use: "capture"}
	capture.PersistentFlags().Bool("no-scroll", false, "")
	capture.PersistentFlags().String("out", "", "")
	capture.Flags().BoolP("background", "d", false, "")
	teams := &cobra.Command{Use: "teams"}
	start := &cobra.Command{Use: "start", RunE: func(c *cobra.Command, _ []string) error {
		got := BackgroundArgs(c, nil)
		want := []string{"teams", "capture", "--no-scroll=true", "--out=/tmp/a file.jsonl", "--profile=work"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("args = %q, want %q", got, want)
		}
		return nil
	}}
	root.AddCommand(teams)
	teams.AddCommand(capture)
	capture.AddCommand(start)
	root.SetArgs([]string{"--profile=work", "teams", "capture", "--no-scroll", "start", "--out=/tmp/a file.jsonl"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	capture.RunE = func(c *cobra.Command, _ []string) error {
		for _, a := range BackgroundArgs(c, nil) {
			if a == "--background=true" {
				t.Fatal("recursive background flag retained")
			}
		}
		return nil
	}
	root.SetArgs([]string{"teams", "capture", "-d"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
}
