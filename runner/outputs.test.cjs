const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const crypto = require("node:crypto");
const { collectOutputs } = require("./outputs.cjs");

test("collects exact declared bytes and rejects unsafe or partial output", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "atoms-output-test-"));
  t.after(() => {
    assert.ok(
      path
        .resolve(root)
        .startsWith(
          path.resolve(os.tmpdir()) + path.sep + "atoms-output-test-",
        ),
    );
    return fs.rm(root, { recursive: true, force: true });
  });
  const bytes = Buffer.from([0, 255, 128, 10, 13, 0, 7]);
  await fs.writeFile(path.join(root, "binary.bin"), bytes);
  const declaration = { path: "binary.bin", format: "binary", old_hash: "" };
  const [file] = await collectOutputs(root, [declaration]);
  assert.deepEqual(Buffer.from(file.data_base64, "base64"), bytes);
  assert.equal(
    file.sha256,
    crypto.createHash("sha256").update(bytes).digest("hex"),
  );
  for (const bad of [
    "../x",
    "/etc/passwd",
    "a/../b",
    "a\\b",
    "",
    "node_modules/x",
  ]) {
    await assert.rejects(collectOutputs(root, [{ ...declaration, path: bad }]));
  }
  await assert.rejects(collectOutputs(root, [declaration, declaration]));
  await assert.rejects(
    collectOutputs(root, [declaration, { ...declaration, path: "missing" }]),
  );
  await assert.rejects(
    collectOutputs(root, [{ ...declaration, format: "text" }]),
  );
  await fs.writeFile(path.join(root, "large.bin"), Buffer.alloc(262145));
  await assert.rejects(
    collectOutputs(root, [{ ...declaration, path: "large.bin" }]),
  );
  await fs.link(path.join(root, "binary.bin"), path.join(root, "linked.bin"));
  await assert.rejects(
    collectOutputs(root, [{ ...declaration, path: "linked.bin" }]),
  );
  if (process.platform !== "win32") {
    await fs.symlink("/etc/passwd", path.join(root, "escape"));
    await assert.rejects(
      collectOutputs(root, [{ ...declaration, path: "escape" }]),
    );
  }
});
