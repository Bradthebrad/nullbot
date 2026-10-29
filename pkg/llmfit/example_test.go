package llmfit_test

import (
	"context"
	"fmt"
	"time"

	"github.com/Bradthebrad/nullbot/pkg/llmfit"
)

func ExampleRank() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	hw, err := llmfit.Detect(ctx)
	if err != nil {
		fmt.Println("hardware detection:", err)
	}
	fits := llmfit.Rank(hw, llmfit.Catalog(), llmfit.Options{
		Runtime: llmfit.RuntimeOllama,
		UseCase: llmfit.UseCoding,
		Limit:   5,
	})
	for _, f := range fits {
		fmt.Printf("%-45s %-9s %-7s %5.1f tok/s  ollama pull %s\n",
			f.Model.Name, f.Level, f.Quant, f.TokensPerSecond, f.OllamaTag)
	}
}
