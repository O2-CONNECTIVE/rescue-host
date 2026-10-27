"""Static token validation, not a behavior-TDD or native-runtime test."""

import re
from pathlib import Path

ROOT = Path(__file__).resolve().parent


def read_tokens():
    source = (ROOT / "tokens.css").read_text(encoding="utf-8")
    light = source.split(":root {", 1)[1].split("}", 1)[0]
    pairs = re.findall(r"(--[\w-]+)\s*:\s*([^;]+);", light)
    tokens = dict(pairs)
    assert len(tokens) == len(pairs), "Duplicate token name"
    for name, value in pairs:
        assert name.startswith(("--ref-", "--sys-", "--cmp-")), name
        refs = re.findall(r"var\((--[\w-]+)\)", value)
        if name.startswith("--ref-"):
            assert not refs, "Foundation must contain literal values: " + name
        elif name.startswith("--sys-"):
            assert all(ref.startswith("--ref-") for ref in refs), name
        else:
            # Component metrics may be literal; aliases must target semantic roles.
            assert all(ref.startswith("--sys-") for ref in refs), name
    for file in ("components.css", "preview.css"):
        for ref in re.findall(r"var\((--[\w-]+)\)", (ROOT / file).read_text()):
            assert not ref.startswith("--ref-"), "Bypassed semantic role: " + ref
            assert ref in tokens, "Unknown token: " + ref
    return tokens


def resolve(tokens, name, trail=()):
    assert name in tokens, "Unknown token: " + name
    assert name not in trail, "Token cycle: " + name
    return re.sub(r"var\((--[\w-]+)\)",
                  lambda match: resolve(tokens, match[1], trail + (name,)),
                  tokens[name])


def luminance(color):
    assert re.fullmatch(r"#[0-9a-fA-F]{6}", color), color
    channels = [int(color[i:i + 2], 16) / 255 for i in (1, 3, 5)]
    linear = [c / 12.92 if c <= 0.04045 else ((c + 0.055) / 1.055) ** 2.4
              for c in channels]
    return sum(c * weight for c, weight in zip(linear, (0.2126, 0.7152, 0.0722)))


def contrast(first, second):
    light, dark = sorted((luminance(first), luminance(second)), reverse=True)
    return (light + 0.05) / (dark + 0.05)


def main():
    tokens = read_tokens()
    for name in tokens:
        resolve(tokens, name)
    # Known WCAG reference values check the measurement independently.
    assert contrast("#000000", "#ffffff") == 21
    assert contrast("#155a92", "#155a92") == 1
    pairs = []
    for fg in ("text-primary", "text-secondary"):
        for bg in ("surface-canvas", "surface-default", "surface-subtle"):
            pairs.append((fg, bg, 4.5))
    for role in ("danger", "warning", "info", "confirmation", "neutral"):
        pairs.append((role + "-fg", role + "-surface", 4.5))
    for bg in ("action-primary", "action-primary-hover"):
        pairs.append(("action-on-primary", bg, 4.5))
    for bg in ("danger-action", "danger-hover"):
        pairs.append(("danger-on-action", bg, 4.5))
    pairs.append(("disabled-fg", "disabled-surface", 4.5))
    for bg in ("surface-default", "surface-canvas", "surface-subtle"):
        pairs.append(("border-default", bg, 3))
        pairs.append(("focus-ring", bg, 3))
    pairs.append(("disabled-border", "disabled-surface", 3))
    print("| Foreground / boundary | Background | Contrast | Minimum |")
    print("| --- | --- | ---: | ---: |")
    failed = []
    for fg, bg, minimum in pairs:
        value = contrast(resolve(tokens, "--sys-" + fg), resolve(tokens, "--sys-" + bg))
        print("| {} | {} | {:.2f}:1 | {}:1 |".format(fg, bg, value, minimum))
        if value < minimum:
            failed.append((fg, bg, value))
    assert not failed, "Contrast below target: " + repr(failed)
    print("\nPASS: {} tokens; {} contrast pairs. Light theme only.".format(len(tokens), len(pairs)))


if __name__ == "__main__":
    main()
