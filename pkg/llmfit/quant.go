package llmfit

// Quantization ladders, best quality first. Analysis walks a ladder and takes
// the first level whose memory estimate fits.
var (
	// quantHierarchy is the GGUF ladder used by llama.cpp and Ollama.
	quantHierarchy = []string{"Q8_0", "Q6_K", "Q5_K_M", "Q4_K_M", "Q3_K_M", "Q2_K"}
	// mlxQuantHierarchy is what MLX builds are published at.
	mlxQuantHierarchy = []string{"mlx-8bit", "mlx-4bit"}
	// ternaryQuantHierarchy: BitNet-style models ship one i2_s build.
	ternaryQuantHierarchy = []string{"I2_S"}
	// mxfp4QuantHierarchy: gpt-oss keeps its MXFP4 expert weights in every
	// GGUF, so walking the K-quant ladder would price weights that don't exist.
	mxfp4QuantHierarchy = []string{"MXFP4"}
	// onnxQuantHierarchy covers llmfit's ONNX seed catalog.
	onnxQuantHierarchy = []string{"Q8_0", "Q4_0"}
)

// The Unsloth "dynamic" quants (UD-Q4_K_XL and friends) size and behave like
// the plain K-quant of the same bit width.
func udBase(quant string) (string, bool) {
	switch quant {
	case "UD-Q2_K_XL", "UD-Q2_K_L", "UD-Q2_K_M", "UD-Q2_K_S":
		return "Q2_K", true
	case "UD-Q3_K_XL", "UD-Q3_K_L", "UD-Q3_K_M", "UD-Q3_K_S":
		return "Q3_K_M", true
	case "UD-Q4_K_XL", "UD-Q4_K_L", "UD-Q4_K_M", "UD-Q4_K_S":
		return "Q4_K_M", true
	case "UD-Q5_K_XL", "UD-Q5_K_L", "UD-Q5_K_M", "UD-Q5_K_S":
		return "Q5_K_M", true
	case "UD-Q6_K_XL", "UD-Q6_K_L", "UD-Q6_K_M", "UD-Q6_K_S":
		return "Q6_K", true
	case "UD-Q8_K_XL", "UD-Q8_K_L", "UD-Q8_K_M", "UD-Q8_K_S":
		return "Q8_0", true
	}
	return "", false
}

// quantBPP is the whole-model size in bytes per parameter at a quantization,
// used for memory sizing. It includes the higher-precision embeddings and
// scales a real file carries, so it runs above the nominal bit width.
func quantBPP(quant string) float64 {
	if base, ok := udBase(quant); ok {
		quant = base
	}
	switch quant {
	case "F32":
		return 4.0
	case "F16", "BF16":
		return 2.0
	case "Q8_0":
		return 1.05
	case "Q6_K":
		return 0.80
	case "Q5_K_M":
		return 0.68
	case "Q4_K_M", "Q4_0":
		return 0.58
	case "Q3_K_M":
		return 0.48
	case "Q2_K":
		return 0.37
	case "I2_S", "TQ2_0", "TQ1_0":
		// Measured from released ternary GGUFs; f16 embeddings dominate.
		return 0.42
	case "MXFP4":
		// gpt-oss-120b's GGUF is 63.4 GB for 116.8B parameters.
		return 0.55
	case "mlx-4bit":
		return 0.55
	case "mlx-8bit":
		return 1.0
	case "AWQ-4bit", "GPTQ-Int4", "AutoRound-4bit":
		return 0.5
	case "AWQ-8bit", "GPTQ-Int8", "AutoRound-8bit":
		return 1.0
	}
	return 0.58
}

// quantBytesPerParam is the nominal weight width the speed estimate reads
// per token: memory bandwidth divided by this many bytes per parameter.
func quantBytesPerParam(quant string) float64 {
	if base, ok := udBase(quant); ok {
		quant = base
	}
	switch quant {
	case "F32":
		return 4.0
	case "F16", "BF16":
		return 2.0
	case "Q8_0":
		return 1.0
	case "Q6_K":
		return 0.75
	case "Q5_K_M":
		return 0.625
	case "Q4_K_M", "Q4_0":
		return 0.5
	case "Q3_K_M":
		return 0.375
	case "Q2_K":
		return 0.25
	case "I2_S", "TQ2_0", "TQ1_0":
		return 0.40
	case "MXFP4":
		// 4-bit values plus one shared 8-bit scale per 32.
		return 0.53
	case "mlx-4bit", "AWQ-4bit", "GPTQ-Int4", "AutoRound-4bit":
		return 0.5
	case "mlx-8bit", "AWQ-8bit", "GPTQ-Int8", "AutoRound-8bit":
		return 1.0
	}
	return 0.5
}

// quantSpeedMultiplier scales the fallback (no bandwidth known) speed
// estimate: smaller weights decode faster.
func quantSpeedMultiplier(quant string) float64 {
	if base, ok := udBase(quant); ok {
		quant = base
	}
	switch quant {
	case "F32":
		return 0.3
	case "F16", "BF16":
		return 0.6
	case "Q8_0":
		return 0.8
	case "Q6_K":
		return 0.95
	case "Q5_K_M":
		return 1.0
	case "Q4_K_M", "Q4_0", "MXFP4", "mlx-4bit":
		return 1.15
	case "Q3_K_M":
		return 1.25
	case "Q2_K":
		return 1.35
	case "I2_S", "TQ2_0", "TQ1_0":
		return 1.3
	case "mlx-8bit", "AWQ-8bit", "GPTQ-Int8", "AutoRound-8bit":
		return 0.85
	case "AWQ-4bit", "GPTQ-Int4", "AutoRound-4bit":
		return 1.2
	}
	return 1.0
}

// quantQualityPenalty is the quality points lost to quantization.
func quantQualityPenalty(quant string) float64 {
	if base, ok := udBase(quant); ok {
		quant = base
	}
	switch quant {
	case "F32", "F16", "BF16", "Q8_0", "mlx-8bit", "AWQ-8bit", "GPTQ-Int8", "AutoRound-8bit":
		return 0
	case "MXFP4":
		// The precision the model was trained and released in.
		return 0
	case "Q6_K":
		return -1
	case "Q5_K_M":
		return -2
	case "Q4_K_M", "Q4_0":
		return -5
	case "Q3_K_M":
		return -8
	case "Q2_K":
		return -12
	case "I2_S", "TQ2_0", "TQ1_0":
		// Trained ternary keeps far more quality than naive 2-bit rounding.
		return -6
	case "mlx-4bit":
		return -4
	case "AWQ-4bit", "GPTQ-Int4", "AutoRound-4bit":
		return -3
	}
	return -5
}
