"""Turn benchstat rows into Go's slash-separated benchmark matcher."""
import re
import sys


def confirmation_pattern(rows):
    patterns = []
    for row in rows:
        fields = row.split()
        if not fields:
            continue
        name = re.sub(r"-[0-9]+$", "", fields[0])
        # Go splits only top-level slashes and alternatives. A group around
        # a complete path instead matches that path against the root alone.
        parts = name.split("/")
        parts[0] = "Benchmark" + parts[0]
        escaped = [re.sub(r"([\\.+*?()|\[\]{}^$])", r"\\\1", part)
                   for part in parts]
        patterns.append("/".join("^" + part + "$" for part in escaped))
    return "|".join(patterns)


if __name__ == "__main__":
    print(confirmation_pattern(sys.stdin))
