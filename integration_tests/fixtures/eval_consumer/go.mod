module example.com/evalconsumer

go 1.25.5

require (
	github.com/stretchr/testify v1.11.1
	goa.design/goa-ai v0.0.0
	goa.design/goa/v3 v3.28.1-0.20260729023504-86484276fd41
)

replace goa.design/goa-ai => ../../..
