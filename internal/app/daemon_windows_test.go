package app

import "testing"

func TestExtractDataDirFromCmdLine(t *testing.T) {
	tests := []struct {
		name    string
		cmdline string
		want    string
	}{
		{"no flag", `C:\cs-cloud.exe serve`, ""},
		{"equals sign", `C:\cs-cloud.exe --data-dir=D:\mydata serve`, "D:\\mydata"},
		{"space separator", `C:\cs-cloud.exe --data-dir D:\mydata serve`, "D:\\mydata"},
		{"quoted path", `C:\cs-cloud.exe --data-dir="D:\My Data" serve`, "D:\\My Data"},
		{"quoted with space prefix", `C:\cs-cloud.exe --data-dir "D:\My Data" serve`, "D:\\My Data"},
		{"flag at end", `C:\cs-cloud.exe serve --data-dir=/tmp/x`, "/tmp/x"},
		{"empty value after equals", `C:\cs-cloud.exe --data-dir= serve`, "serve"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractDataDirFromCmdLine(tt.cmdline)
			if got != tt.want {
				t.Errorf("extractDataDirFromCmdLine(%q) = %q, want %q", tt.cmdline, got, tt.want)
			}
		})
	}
}
