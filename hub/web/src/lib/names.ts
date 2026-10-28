// The name the spawn dialog suggests for a new agent, the way Docker names containers: the template, a word pair and a number, such
// as coder-sunnycar2332. Not a number alone: a removed agent keeps its branch, so a name such as coder-3 is often refused (CLAUDE.md,
// "Auto-chosen instance names skip numbers"), and the hub cannot see the branches. With a word pair and a number, a clash is rare, and
// fails like any other spawn error. The words are short and plain, and no pair of them reads as anything unfortunate.

const adjectives = [
  "amber", "azure", "bold", "brave", "breezy", "bright", "calm", "clever", "cosmic", "coral", "crisp", "dapper", "eager", "fancy",
  "frosty", "fuzzy", "gentle", "glad", "golden", "grand", "happy", "jolly", "keen", "kind", "lively", "lucky", "mellow", "merry",
  "misty", "nimble", "noble", "plucky", "polar", "proud", "quick", "quiet", "rapid", "rosy", "shiny", "silver", "sleek", "smooth",
  "snowy", "spry", "steady", "sturdy", "sunny", "swift", "tidy", "tiny", "vivid", "warm", "witty", "zesty",
];

const nouns = [
  "acorn", "badger", "beacon", "birch", "canyon", "car", "cedar", "cloud", "comet", "coyote", "crane", "delta", "dune", "ember",
  "falcon", "fern", "finch", "fjord", "garnet", "glacier", "harbor", "heron", "island", "juniper", "kayak", "kite", "koala",
  "lantern", "lynx", "maple", "meadow", "nebula", "orbit", "orca", "otter", "panda", "pebble", "pine", "prairie", "puffin",
  "quartz", "raven", "reef", "river", "rocket", "sparrow", "summit", "tulip", "valley", "walrus", "willow", "yak",
];

const pick = (list: string[], random: () => number) => list[Math.floor(random() * list.length)]!;

/** A name for a new agent of the template that none of `taken` has, within the 63 characters a name may have. */
export function suggestName(template: string, taken: Iterable<string> = [], random: () => number = Math.random): string {
  const used = new Set(taken);
  let name = "";
  for (let i = 0; i < 20; i++) {
    const tail = `${pick(adjectives, random)}${pick(nouns, random)}${1000 + Math.floor(random() * 9000)}`;
    name = `${template.slice(0, 63 - tail.length - 1)}-${tail}`;
    if (!used.has(name)) break;
  }
  return name;
}
