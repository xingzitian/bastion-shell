package main

// version 程序版本。
//
// ⚠️ 必须和 wails.json 的 `Info.productVersion` 以及 frontend/package.json 的 version 一致 ——
// 它会被写进 MCP 的 serverInfo 里给 AI 看，版本对不上会让「你用的是哪个版本」变成猜谜。
// 有测试盯着这三处（TestVersionMatchesWailsJSON），改了记得一起改。
const version = "0.9.0"
