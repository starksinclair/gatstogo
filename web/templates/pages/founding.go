package pages

// foundingBadgeLabel is the badge a plant earns by being among the first 50
// applications: the first three get their own, the rest are Founding Plants.
// rank 0 means no badge.
func foundingBadgeLabel(rank int) string {
	switch {
	case rank <= 0:
		return ""
	case rank == 1:
		return "1st Founding Plant"
	case rank == 2:
		return "2nd Founding Plant"
	case rank == 3:
		return "3rd Founding Plant"
	default:
		return "Founding Plant"
	}
}
