// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

// PityBand is a display-only classification of an already reported draw.
type PityBand string

const (
	Regular             PityBand = "常规"
	Boosted             PityBand = "概率提升"
	Guaranteed          PityBand = "保底"
	Abnormal            PityBand = "异常"
	CharacterHardPity            = 90
	CharacterBoostStart          = 71
	WeaponHardPity               = 60
	// WeaponLimitedGuarantee spans draws and never classifies individual records.
	WeaponLimitedGuarantee = 80
)

type poolRule struct{ hard, boost int }

func rule(p Pool) poolRule {
	switch p {
	case Limited, Permanent:
		return poolRule{CharacterHardPity, CharacterBoostStart}
	case Weapon:
		return poolRule{WeaponHardPity, 0}
	}
	return poolRule{}
}

// HardPity returns zero for an unknown pool.
func HardPity(p Pool) int { return rule(p).hard }

// BoostStart is the first draw of the raised-rate zone, or zero when the pool has none.
func BoostStart(p Pool) int { return rule(p).boost }

// Band classifies anomalies without changing the stored pity.
func Band(p Pool, pity int) PityBand {
	r := rule(p)
	switch {
	case r.hard == 0 || pity < 1 || pity > r.hard:
		return Abnormal
	case pity == r.hard:
		return Guaranteed
	case r.boost > 0 && pity >= r.boost:
		return Boosted
	default:
		return Regular
	}
}

// Ratio uses the local rule table, clamped to [0,1], with zero for unknown pools.
func Ratio(p Pool, pity int) float64 {
	hard := HardPity(p)
	if hard == 0 || pity <= 0 {
		return 0
	}
	if pity >= hard {
		return 1
	}
	return float64(pity) / float64(hard)
}
