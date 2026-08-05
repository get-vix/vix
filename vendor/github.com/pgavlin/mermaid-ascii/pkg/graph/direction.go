package graph

type direction genericCoord

// Direction constants for edge routing and arrow placement.
var (
	Up         = direction{1, 0}
	Down       = direction{1, 2}
	Left       = direction{0, 1}
	Right      = direction{2, 1}
	UpperRight = direction{2, 0}
	UpperLeft  = direction{0, 0}
	LowerRight = direction{2, 2}
	LowerLeft  = direction{0, 2}
	Middle     = direction{1, 1}
)

func (d direction) getOpposite() direction {
	switch d {
	case Up:
		return Down
	case Down:
		return Up
	case Left:
		return Right
	case Right:
		return Left
	case UpperRight:
		return LowerLeft
	case UpperLeft:
		return LowerRight
	case LowerRight:
		return UpperLeft
	case LowerLeft:
		return UpperRight
	case Middle:
		return Middle
	default:
		return Middle
	}
}

func (c gridCoord) Direction(dir direction) gridCoord {
	return gridCoord{x: c.x + dir.x, y: c.y + dir.y}
}
func (c drawingCoord) Direction(dir direction) drawingCoord {
	return drawingCoord{x: c.x + dir.x, y: c.y + dir.y}
}

func (g *graph) selfReferenceDirection(e *edge) (direction, direction, direction, direction) {
	switch g.graphDirection {
	case "LR":
		return Right, Down, Down, Right
	case "RL":
		return Left, Down, Down, Left
	case "BT":
		return Up, Right, Right, Up
	default: // TD
		return Down, Right, Right, Down
	}
}

func (g *graph) determineStartAndEndDir(e *edge) (direction, direction, direction, direction) {
	if e.from == e.to {
		return g.selfReferenceDirection(e)
	}
	d := determineDirection(genericCoord(*e.from.gridCoord), genericCoord(*e.to.gridCoord))
	var preferredDir, preferredOppositeDir, alternativeDir, alternativeOppositeDir direction

	// Normalize direction for layout logic: BT behaves like TD, RL like LR
	// but with mirrored "backwards" detection
	isHorizontalLayout := g.graphDirection == "LR" || g.graphDirection == "RL"

	// Check if this is a backwards flowing edge
	isBackwards := false
	if g.graphDirection == "LR" {
		isBackwards = (d == Left || d == UpperLeft || d == LowerLeft)
	} else if g.graphDirection == "RL" {
		isBackwards = (d == Right || d == UpperRight || d == LowerRight)
	} else if g.graphDirection == "BT" {
		isBackwards = (d == Down || d == LowerLeft || d == LowerRight)
	} else { // TD mode
		isBackwards = (d == Up || d == UpperLeft || d == UpperRight)
	}

	// For backwards edge routing, determine the "detour" direction
	// LR: detour via Down, RL: detour via Down, TD: detour via Right, BT: detour via Right
	backwardsDetourDir := Right // TD default
	if isHorizontalLayout {
		backwardsDetourDir = Down
	}
	if g.graphDirection == "BT" {
		backwardsDetourDir = Right
	}

	// LR/RL: prefer vertical over horizontal
	// TD/BT: prefer horizontal over vertical
	switch d {
	case LowerRight:
		if isHorizontalLayout {
			preferredDir = Down
			preferredOppositeDir = Left
			alternativeDir = Right
			alternativeOppositeDir = Up
		} else {
			preferredDir = Right
			preferredOppositeDir = Up
			alternativeDir = Down
			alternativeOppositeDir = Left
		}
	case UpperRight:
		if isHorizontalLayout {
			preferredDir = Up
			preferredOppositeDir = Left
			alternativeDir = Right
			alternativeOppositeDir = Down
		} else {
			preferredDir = Right
			preferredOppositeDir = Down
			alternativeDir = Up
			alternativeOppositeDir = Left
		}
	case LowerLeft:
		if isHorizontalLayout {
			preferredDir = Down
			preferredOppositeDir = Down
			alternativeDir = Left
			alternativeOppositeDir = Up
		} else {
			preferredDir = Left
			preferredOppositeDir = Up
			alternativeDir = Down
			alternativeOppositeDir = Right
		}
	case UpperLeft:
		if isHorizontalLayout {
			preferredDir = Down
			preferredOppositeDir = Down
			alternativeDir = Left
			alternativeOppositeDir = Down
		} else {
			preferredDir = Right
			preferredOppositeDir = Right
			alternativeDir = Up
			alternativeOppositeDir = Right
		}
	default:
		// Handle direct backwards flow cases
		if isBackwards {
			if isHorizontalLayout && (d == Left || d == Right) {
				preferredDir = backwardsDetourDir
				preferredOppositeDir = backwardsDetourDir
				alternativeDir = d
				alternativeOppositeDir = d.getOpposite()
			} else if !isHorizontalLayout && (d == Up || d == Down) {
				preferredDir = backwardsDetourDir
				preferredOppositeDir = backwardsDetourDir
				alternativeDir = d
				alternativeOppositeDir = d.getOpposite()
			} else {
				preferredDir = d
				preferredOppositeDir = preferredDir.getOpposite()
				alternativeDir = d
				alternativeOppositeDir = preferredOppositeDir
			}
		} else {
			preferredDir = d
			preferredOppositeDir = preferredDir.getOpposite()
			// TODO: just return null and don't calculate alternative path
			alternativeDir = d
			alternativeOppositeDir = preferredOppositeDir
		}
	}
	return preferredDir, preferredOppositeDir, alternativeDir, alternativeOppositeDir
}
