"""Offline tests of the llms.txt check: python3 scripts/test-check-llms-txt.py.

Each test builds a small docs tree and a small built site in a temporary
folder, breaks one thing, and runs scripts/check-llms-txt.py there.
"""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

CHECK = Path(__file__).resolve().parent / "check-llms-txt.py"
SITE = "https://docs.example.invalid/v1.2/"

SOURCES = {
    "go.mod": "module github.com/example/project\n\ngo 1.26\n",
    "README.md": "# Tool\n\nRun agents. See [install](docs/how-to/install.md) and [the script](install.sh).\n",
    "docs/how-to/install.md": "# Install\n\nInstall it with `tool install [x](y.md)`. Go [home](../../README.md) or see [a screen](../images/a.svg).\n\n"
                              "```sh\n[not a link](nowhere.md)\n```\n",
    "docs/reference/x.md": "---\ntitle: \"Tool x\"\n---\n\n<!-- Generated. -->\n\nThe x command.\n",
}
PAGES = {"README.md": "/", "docs/how-to/install.md": "/how-to/install/", "docs/how-to": "/how-to/install/",
         "docs/reference/x.md": "/reference/x/", "docs/project/README.md": "/project/"}
COPIES = {
    "index.md": "# Tool\n\nRun agents. See [install](how-to/install.md) and [the script](https://github.com/example/project/blob/main/install.sh).\n",
    "how-to/install.md": "# Install\n\nInstall it with `tool install [x](y.md)`. Go [home](../index.md) or see [a screen](../images/a.svg).\n\n"
                         "```sh\n[not a link](nowhere.md)\n```\n",
    "reference/x.md": "# Tool x\n\n<!-- Generated. -->\n\nThe x command.\n",
    "project.md": "# The project\n\nHow it is run. See the [changelog](https://example.invalid/changes).\n",
    "images/a.svg": "<svg/>",
}
LLMS = ("# Tool\n\n> Runs agents.\n\nEach link goes to a copy.\n\n## Docs\n\n"
        f"- [Tool]({SITE}index.md): Run agents.\n- [Install]({SITE}how-to/install.md): Install it.\n\n"
        f"## Reference\n\n- [Tool x]({SITE}reference/x.md): The x command.\n\n"
        f"## Optional\n\n- [The project]({SITE}project.md): How it is run.\n")


class CheckTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.public = self.root / "build" / "public"
        for path, text in SOURCES.items():
            self.write(path, text)
        for path, text in COPIES.items():
            self.write("build/public/" + path, text)
        self.write("build/public/llms.txt", LLMS)
        self.write("pages.json", json.dumps(PAGES))

    def write(self, path, text):
        p = self.root / path
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text, encoding="utf-8")

    def edit(self, path, old, new):
        p = self.root / path
        text = p.read_text(encoding="utf-8")
        self.assertIn(old, text)
        p.write_text(text.replace(old, new), encoding="utf-8")

    def check(self, code, *words):
        r = subprocess.run([sys.executable, str(CHECK), "build/public", "pages.json"], cwd=self.root,
                           capture_output=True, text=True, timeout=60)
        self.assertEqual(r.returncode, code, r.stdout + r.stderr)
        for word in words:
            self.assertIn(word, r.stdout)

    def test_good(self):
        self.check(0, "check-llms-txt: ok (4 pages")

    def test_wrong_title(self):
        self.edit("build/public/how-to/install.md", "# Install\n", "# Wrong title\n")
        self.check(1, "how-to/install.md: the title '# Wrong title'")

    def test_wrong_title_from_front_matter(self):
        self.edit("build/public/reference/x.md", "# Tool x\n", "# x\n")
        self.check(1, "reference/x.md: the title")

    def test_link_to_an_html_page(self):
        self.edit("build/public/how-to/install.md", "(../index.md)", "(../)")
        self.check(1, "how-to/install.md: a link leads to ../, which is not a Markdown copy or an image")

    def test_link_to_a_page_address(self):
        self.edit("build/public/index.md", "(how-to/install.md)", "(how-to/install/)")
        self.check(1, "index.md: a link leads to how-to/install/, which is not a Markdown copy")

    def test_broken_link(self):
        self.edit("build/public/how-to/install.md", "(../index.md)", "(../nowhere.md)")
        self.check(1, "a link leads to ../nowhere.md, which does not exist")

    def test_broken_link_in_a_generated_page(self):
        self.edit("build/public/project.md", "(https://example.invalid/changes)", "(project/gone.md)")
        self.check(1, "project.md: a link leads to project/gone.md, which does not exist")

    def test_link_out_of_the_build(self):
        self.edit("build/public/index.md", "(how-to/install.md)", "(../../README.md)")
        self.check(1, "leads out of the build")

    def test_text_differs(self):
        self.edit("build/public/how-to/install.md", "Go [home]", "Go back [home]")
        self.check(1, "how-to/install.md does not match its source docs/how-to/install.md, from line 3")

    def test_code_block_differs(self):
        self.edit("build/public/how-to/install.md", "(nowhere.md)", "(CORRUPTED-EXAMPLE.md)")
        self.check(1, "how-to/install.md does not match its source docs/how-to/install.md, from line 6")

    def test_code_span_differs(self):
        self.edit("build/public/how-to/install.md", "`tool install [x](y.md)`", "`tool install [x](z.md)`")
        self.check(1, "how-to/install.md does not match its source")

    def test_link_to_a_wrong_copy_that_exists(self):
        self.edit("build/public/how-to/install.md", "(../index.md)", "(../reference/x.md)")
        self.check(1, "how-to/install.md: the link to ../../README.md in docs/how-to/install.md leads to ../reference/x.md, want ../index.md")

    def test_link_to_a_wrong_image_that_exists(self):
        self.write("build/public/images/b.svg", "<svg/>")
        self.edit("build/public/how-to/install.md", "(../images/a.svg)", "(../images/b.svg)")
        self.check(1, "leads to ../images/b.svg, want ../images/a.svg")

    def test_link_to_the_wrong_file_on_github(self):
        self.edit("build/public/index.md", "/blob/main/install.sh", "/tree/main/install.sh")
        self.check(1, "want https://github.com/example/project/blob/main/install.sh")

    def test_missing_description(self):
        self.edit("build/public/llms.txt", "install.md): Install it.", "install.md)")
        self.check(1, "how-to/install.md has no description")

    def test_listed_twice(self):
        self.edit("build/public/llms.txt", "\n## Reference", f"- [Install]({SITE}how-to/install.md): Again.\n\n## Reference")
        self.check(1, "how-to/install.md is listed twice")

    def test_missing_page(self):
        self.edit("build/public/llms.txt", f"- [Tool x]({SITE}reference/x.md): The x command.\n", "")
        self.check(1, "llms.txt misses /reference/x/")

    def test_missing_copy(self):
        (self.public / "reference" / "x.md").unlink()
        self.check(1, "the copy reference/x.md is missing")

    def test_first_item_not_the_landing_page(self):
        self.edit("build/public/llms.txt", f"- [Tool]({SITE}index.md): Run agents.\n", "")
        self.check(1, "the first item is not the landing page's copy")


if __name__ == "__main__":
    unittest.main()
