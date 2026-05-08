// Package names provides deterministic task name generation using adjective-animal format.
package names

import "github.com/oklog/ulid/v2"

var adjectives = []string{
	"calm", "fierce", "quiet", "bold", "swift", "bright", "dark", "tall",
	"cold", "warm", "brave", "noble", "sharp", "soft", "loud", "gentle",
	"ancient", "modern", "tiny", "giant", "silver", "golden", "crimson",
	"azure", "jade", "amber", "cosmic", "primal", "lunar", "solar", "stellar",
	"cryptic", "vivid", "frozen", "molten", "stellar", "crystal", "velvet",
	"iron", "bronze", "copper", "marble", "granite", "silken", "rustic",
	"elegant", "mystic", "royal", "wild", "sacred", "hidden", "sacred",
	"noble", "soaring", "graceful", "mighty", "deft", "agile",
}

var animals = []string{
	"marmot", "ibis", "crane", "gecko", "bison", "heron", "lynx", "otter",
	"raven", "swift", "vole", "wren", "yak", "zebu", "tapir", "quoll",
	"pika", "newt", "mink", "kite", "jay", "ibex", "dhole", "coot",
	"brant", "tern", "oriole", "quail", "stilt", "shrike", "titmouse",
	"chickadee", "nuthatch", "widgeon", "gannet", "petrel", "skua",
	"gecko", "skink", " agama", "chameleon", " gekko",
	"wallaby", "bandicoot", "potoroo", "wallaroo", "pademelon",
	"alpaca", "llama", "vicuna", "guanaco", "narwhal", "dugong",
	"manatee", "ocelot", "caracal", "serval", "margay", "oncilla",
}

// Generate returns a deterministic adjective-animal name for the given task ID.
// Uses the ULID bytes as a seed so the same ID always produces the same name.
func Generate(id string) string {
	u, err := ulid.Parse(id)
	if err != nil {
		// Fallback to simple hash if ID is not a valid ULID
		return "mystery-marmot"
	}
	b := u.Bytes()
	// Use first 8 bytes as a numeric seed for deterministic selection
	seed := uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
		uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])

	adjIdx := int(seed % uint64(len(adjectives)))
	// Shift seed for animal selection
	animalSeed := seed >> 8
	animalIdx := int(animalSeed % uint64(len(animals)))

	return adjectives[adjIdx] + "-" + animals[animalIdx]
}
