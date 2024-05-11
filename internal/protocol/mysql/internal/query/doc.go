// Package query 是 cmd 为 cmd.CmdQuery 对应解析出来的东西
// 这部分内容应为非常通用，所以我们需要单独定义出来
// 后续支持别的数据库，这个包可以从 mysql 中挪出去，作为所有的数据库都具备的一个东西
// 并且可以考虑定义为接口
// 你可以理解为常规意义上我们说的 SQL 查询语句
package query
