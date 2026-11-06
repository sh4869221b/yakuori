module github.com/sh4869221b/yakuori

go 1.27.3

require (
	github.com/ebitengine/purego v0.11.2
	github.com/eitamring/gocudrv v0.3.2
	github.com/townsendmerino/goinfer v0.24.1
	github.com/townsendmerino/goinfer/backend/cuda v0.0.0-20261106011512-994cbf5ee38d
	golang.org/x/sys v0.49.0
	modernc.org/sqlite v1.61.1
)

require (
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/townsendmerino/aikit v1.76.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	modernc.org/libc v1.80.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

replace github.com/townsendmerino/goinfer => github.com/sh4869221b/goinfer v0.0.0-20261106011512-994cbf5ee38d

replace github.com/townsendmerino/goinfer/backend/cuda => github.com/sh4869221b/goinfer/backend/cuda v0.0.0-20261106011512-994cbf5ee38d
