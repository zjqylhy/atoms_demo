const path = require("node:path");
const esbuild = require("esbuild");
const { safeName } = require("./project-path.cjs");
async function buildHTML(
  job,
  entry,
  { root, nodePaths = ["/opt/atoms/node_modules"] } = {},
) {
  const ROOT = root;
  const dependencies = new Set([entry]);
  const names = Object.keys(job.files || {})
    .concat(Object.keys(job.binary_files || {}))
    .sort();
  const readBytes = (name) =>
    Object.hasOwn(job.files || {}, name)
      ? Buffer.from(job.files[name], "utf8")
      : Buffer.from(job.binary_files[name], "base64");
  function local(ref, base) {
    if (/^(data:|#)/i.test(ref)) return null;
    if (/^[a-z]+:|^\/\//i.test(ref)) throw Error("不支持外部资源：" + ref);
    const name = path.posix.normalize(
      path.posix.join(path.posix.dirname(base), ref.replace(/^\//, "")),
    );
    safeName(name);
    if (!names.includes(name)) throw Error("本地资源不存在：" + name);
    dependencies.add(name);
    return name;
  }

  safeName(entry);
  if (!entry.endsWith(".html") || !job.files[entry])
    throw Error("网页入口不存在");
  const { parse, serialize } = await import("parse5");
  const doc = parse(job.files[entry]);
  let bundled = 0;
  const asset = (ref, base) => {
    const name = local(ref, base);
    if (!name) return ref;
    const ext = path.extname(name);
    const mime = {
      ".svg": "image/svg+xml",
      ".png": "image/png",
      ".jpg": "image/jpeg",
      ".woff2": "font/woff2",
    }[ext];
    if (!mime) throw Error("不支持的图片或字体格式：" + name);
    return `data:${mime};base64,${readBytes(name).toString("base64")}`;
  };
  const css = (text, base) =>
    text.replace(
      /url\(\s*(['"]?)(.*?)\1\s*\)/gi,
      (_, q, ref) => `url("${asset(ref, base)}")`,
    );
  const bundle = async (name, text) => {
    const opts = {
      bundle: true,
      write: false,
      format: "iife",
      platform: "browser",
      target: "es2022",
      minify: true,
      jsx: "automatic",
      nodePaths,
      metafile: true,
      define: { "process.env.NODE_ENV": '"production"' },
      logLevel: "silent",
      loader: {
        ".svg": "dataurl",
        ".png": "dataurl",
        ".jpg": "dataurl",
        ".woff2": "dataurl",
      },
      absWorkingDir: ROOT,
    };
    const res = await esbuild.build(
      name
        ? { ...opts, entryPoints: [path.join(ROOT, name)] }
        : {
            ...opts,
            stdin: {
              contents: text,
              resolveDir: ROOT,
              sourcefile: "inline.js",
              loader: "js",
            },
          },
    );
    for (const input of Object.keys(res.metafile.inputs)) {
      if (input === "inline.js" && !name) continue;
      const absolute = path.resolve(ROOT, input);
      const relative = path.relative(ROOT, absolute).split(path.sep).join("/");
      if (names.includes(relative)) dependencies.add(relative);
      else if (
        !nodePaths.some((dir) =>
          absolute.startsWith(path.resolve(dir) + path.sep),
        )
      )
        throw Error("构建依赖越过可信输入范围：" + input);
    }
    // esbuild can read resolution configuration without listing it in inputs.
    // Retain config contents and the path namespace to cover newly resolvable imports.
    for (const file of names) {
      if (file.endsWith(".json")) dependencies.add(file);
    }
    if (res.outputFiles.length !== 1) throw Error("请通过 HTML link 引用 CSS");
    bundled++;
    return res.outputFiles[0].text.replace(/<\/script/gi, "<\\/script");
  };
  async function visit(node) {
    const attrs = node.attrs || [];
    const attr = (n) => attrs.find((a) => a.name === n);
    const remove = (n) => {
      node.attrs = (node.attrs || []).filter((a) => a.name !== n);
    };
    const setText = (text) => {
      node.childNodes = [{ nodeName: "#text", value: text, parentNode: node }];
    };
    if (
      node.tagName === "base" ||
      node.tagName === "iframe" ||
      node.tagName === "object" ||
      node.tagName === "embed"
    )
      throw Error("不支持嵌入外部页面或修改基础地址");
    if (node.tagName === "meta" && attr("http-equiv"))
      throw Error("不允许 http-equiv 元信息");
    if (node.tagName === "script") {
      const type = attr("type")?.value;
      if (
        !type ||
        ["module", "text/javascript", "application/javascript"].includes(type)
      ) {
        const src = attr("src");
        const name = src ? local(src.value, entry) : null;
        const code = name
          ? await bundle(name)
          : await bundle(
              null,
              (node.childNodes || []).map((n) => n.value || "").join(""),
            );
        remove("src");
        remove("type");
        remove("integrity");
        setText(code);
      }
    }
    if (node.tagName === "link") {
      if (attr("rel")?.value === "stylesheet") {
        const name = local(attr("href")?.value || "", entry);
        if (!name) throw Error("样式引用无效");
        node.tagName = "style";
        node.nodeName = "style";
        node.attrs = [];
        setText(css(job.files[name], name));
      } else if (attr("href"))
        attr("href").value = asset(attr("href").value, entry);
    }
    if (node.tagName === "style") {
      const text = (node.childNodes || []).map((n) => n.value || "").join("");
      if (/@import/i.test(text)) throw Error("不允许 CSS @import");
      setText(css(text, entry));
    }
    if (attr("style")) attr("style").value = css(attr("style").value, entry);
    if (node.tagName === "img" && attr("src"))
      attr("src").value = asset(attr("src").value, entry);
    if (attr("srcset")) throw Error("首版请使用单一 src 图片资源");
    if (
      node.tagName === "a" &&
      attr("href") &&
      !attr("href").value.startsWith("#")
    )
      throw Error("首版网页仅支持页内链接");
    for (const child of node.childNodes || []) await visit(child);
  }
  await visit(doc);
  const html = serialize(doc);
  if (Buffer.byteLength(html) > 1048576) throw Error("构建后网页超过 1 MiB");
  return {
    html,
    bundled,
    dependencies: {
      version: 1,
      precision: "exact",
      paths: [...dependencies].sort(),
      namespace: names,
    },
  };
}
module.exports = { buildHTML };
