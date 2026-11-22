default:
	@go build -o worldhop ./cmd/worldhop
	@./worldhop -store ~/Documents/projects/claude-projects/testing-shared
