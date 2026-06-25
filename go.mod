module github.com/excelano/nved

go 1.25.0

require golang.org/x/term v0.44.0

require (
	github.com/excelano/encsniff-go v0.0.0-00010101000000-000000000000
	golang.org/x/sys v0.46.0
)

replace github.com/excelano/encsniff-go => ../encsniff-go
