function safeName(name) {
  if (
    typeof name !== "string" ||
    name.length > 180 ||
    name.includes("\\") ||
    name.includes(":") ||
    name.startsWith("/") ||
    name
      .split("/")
      .some((p) => !p || p.startsWith(".") || p === "node_modules") ||
    /[\x00-\x1f]/.test(name)
  )
    throw Error("无效项目文件路径");
  return name;
}
module.exports = { safeName };
