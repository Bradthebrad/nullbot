package infra

import "strings"

// Approximate on-demand list prices, in US dollars per hour, in us-east-1
// (AWS) and us-central1 (Google Cloud), for common machine types. They are
// for showing roughly what a plan costs before it is approved, not billing:
// prices change, and other regions differ.
var machinePrices = map[Cloud]map[string]float64{
	AWS: {
		"t3.nano": 0.0052, "t3.micro": 0.0104, "t3.small": 0.0208, "t3.medium": 0.0416, "t3.large": 0.0832, "t3.xlarge": 0.1664, "t3.2xlarge": 0.3328,
		"t4g.nano": 0.0042, "t4g.micro": 0.0084, "t4g.small": 0.0168, "t4g.medium": 0.0336, "t4g.large": 0.0672, "t4g.xlarge": 0.1344, "t4g.2xlarge": 0.2688,
		"m7i.large": 0.1008, "m7i.xlarge": 0.2016, "m7i.2xlarge": 0.4032, "m7i.4xlarge": 0.8064,
		"m7g.large": 0.0816, "m7g.xlarge": 0.1632, "m7g.2xlarge": 0.3264,
		"c7i.large": 0.0893, "c7i.xlarge": 0.1785, "c7i.2xlarge": 0.357, "c7i.4xlarge": 0.714,
		"r7i.large": 0.1323, "r7i.xlarge": 0.2646, "r7i.2xlarge": 0.5292,
		"g4dn.xlarge": 0.526, "g4dn.2xlarge": 0.752, "g4dn.4xlarge": 1.204,
		"g5.xlarge": 1.006, "g5.2xlarge": 1.212, "g5.4xlarge": 1.624, "g5.12xlarge": 5.672,
		"g6.xlarge": 0.8048, "g6.2xlarge": 0.9776, "g6.4xlarge": 1.3232, "g6.12xlarge": 4.6016,
		"g6e.xlarge": 1.861, "g6e.2xlarge": 2.24208, "g6e.12xlarge": 10.49264,
	},
	GCP: {
		"e2-micro": 0.0084, "e2-small": 0.0168, "e2-medium": 0.0335, "e2-standard-2": 0.067, "e2-standard-4": 0.134, "e2-standard-8": 0.268, "e2-standard-16": 0.536,
		"n2-standard-2": 0.0971, "n2-standard-4": 0.1942, "n2-standard-8": 0.3885, "n2-standard-16": 0.7769,
		"c3-standard-4": 0.2015, "c3-standard-8": 0.4029,
		"t2a-standard-1": 0.0385, "t2a-standard-2": 0.077, "t2a-standard-4": 0.154,
		"g2-standard-4": 0.7067, "g2-standard-8": 0.8536, "g2-standard-12": 1.0005, "g2-standard-16": 1.1474, "g2-standard-24": 2.0010,
		"a2-highgpu-1g": 3.6733, "a2-ultragpu-1g": 5.0688,
	},
}

// diskMonthly is the price of a gigabyte of boot disk for a month (gp3 on
// AWS, balanced persistent disk on Google Cloud).
var diskMonthly = map[Cloud]float64{AWS: 0.08, GCP: 0.10}

const hoursPerMonth = 730

// MachinePrice is a machine type's approximate hourly price, and whether it
// is known.
func MachinePrice(cloud Cloud, machineType string) (float64, bool) {
	price, ok := machinePrices[cloud][strings.ToLower(machineType)]
	return price, ok
}

// DiskPrice is the approximate hourly price of a boot disk.
func DiskPrice(cloud Cloud, gb int) float64 {
	return diskMonthly[cloud] * float64(gb) / hoursPerMonth
}
