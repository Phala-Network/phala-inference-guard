package tier

func BasicLimit(globalLimit int) int {
	return BasicLimitWithPremium(globalLimit, 0)
}

func BasicLimitWithPremium(globalLimit int, premiumInflight int64) int {
	if globalLimit <= 0 {
		return 0
	}
	if globalLimit == 1 {
		return 1
	}
	reserved := PremiumReserved(globalLimit, premiumInflight)
	basicLimit := globalLimit - reserved
	if basicLimit < 0 {
		return 0
	}
	return basicLimit
}

func PremiumReserved(globalLimit int, premiumInflight int64) int {
	if globalLimit <= 1 {
		return 0
	}
	reserved := int(premiumInflight) + 1
	if reserved < 1 {
		reserved = 1
	}
	if reserved > globalLimit {
		return globalLimit
	}
	return reserved
}
