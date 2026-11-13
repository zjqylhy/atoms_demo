const fs = require("node:fs/promises");
const { constants } = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");

function safeOutput(name) {
  if (
    typeof name !== "string" ||
    !name ||
    name.length > 180 ||
    name.startsWith("/") ||
    /[\\:\x00-\x1f]/.test(name) ||
    name.split("/").some((p) => !p || p.startsWith(".") || p === "node_modules")
  )
    throw Error("无效输出路径");
  return name;
}

function declarations(outputs = []) {
  if (!Array.isArray(outputs) || outputs.length > 20)
    throw Error("输出声明数量无效");
  const seen = new Set();
  for (const output of outputs) {
    safeOutput(output.path);
    if (
      seen.has(output.path) ||
      !["text", "binary"].includes(output.format) ||
      typeof output.old_hash !== "string"
    )
      throw Error("输出声明无效或重复");
    seen.add(output.path);
  }
  return outputs;
}

// Read only declared regular files beneath the scratch root. Check the opened
// descriptor too, so a path swap cannot turn collection into an arbitrary read.
async function collectOutputs(root, outputs) {
  declarations(outputs);
  const resolvedRoot = await fs.realpath(root);
  const collected = [];
  let total = 0;
  for (const output of outputs) {
    let parent = resolvedRoot;
    for (const component of output.path.split("/")) {
      parent = path.join(parent, component);
      const stat = await fs.lstat(parent);
      if (stat.isSymbolicLink()) throw Error("拒绝回收符号链接");
    }
    const handle = await fs.open(
      parent,
      constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK,
    );
    try {
      const real = await fs.realpath(
        process.platform === "linux" ? `/proc/self/fd/${handle.fd}` : parent,
      );
      if (!real.startsWith(resolvedRoot + path.sep))
        throw Error("输出越过允许目录");
      const before = await handle.stat();
      if (!before.isFile() || before.nlink !== 1 || before.size > 262144)
        throw Error("输出不是普通单链接文件或超过 256 KiB");
      const buffer = Buffer.alloc(262145);
      const { bytesRead } = await handle.read(buffer, 0, buffer.length, 0);
      const after = await handle.stat();
      if (
        bytesRead !== before.size ||
        before.size !== after.size ||
        before.mtimeMs !== after.mtimeMs
      )
        throw Error("回收期间输出发生变化");
      const bytes = buffer.subarray(0, bytesRead);
      total += bytes.length;
      if (total > 2097152) throw Error("回收总大小超过 2 MiB");
      if (
        output.format === "text" &&
        !Buffer.from(bytes.toString("utf8")).equals(bytes)
      )
        throw Error("文本输出不是 UTF-8");
      collected.push({
        path: output.path,
        format: output.format,
        data_base64: bytes.toString("base64"),
        size: bytes.length,
        sha256: crypto.createHash("sha256").update(bytes).digest("hex"),
      });
    } finally {
      await handle.close();
    }
  }
  return collected;
}
module.exports = { declarations, collectOutputs };
