package server

import (
	"database/sql"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
)

type MysqlObj struct {
	db *sql.DB
}

func NewMysqlDB() (mySqlDB *MysqlObj, err error) {

	if mysqlDBObj != nil {
		return
	}

	mySqlDB = &MysqlObj{}
	//打开数据库
	//DSN数据源字符串：用户名:密码@协议(地址:端口)/数据库?参数=参数值
	db, err := sql.Open("mysql", "root:Zeruibighui!@#$1@tcp(127.0.0.1:3306)/ngrok?charset=utf8")
	//db, err := sql.Open("mysql", "root:Zeruibighui!@#$1@tcp(localhost:3306)/ngrok")
	if err != nil {
		fmt.Println("mysql", err)
		return
	}
	mySqlDB.db = db
	return
}

func (mysqlDB *MysqlObj) close() {
	mysqlDB.db.Close()
}

func (mysqlDB *MysqlObj) Query(sqlStr string) (rows *sql.Rows, err error) {
	rows, err = mysqlDB.db.Query(sqlStr)
	if err != nil {
		return nil, err
	}
	return
}

func (mysqlDB *MysqlObj) QueryForRow(sqlStr string, args ...interface{}) (row *sql.Row, err error) {
	row = mysqlDB.db.QueryRow(sqlStr, args...)

	if err != nil {
		return nil, err
	}
	return
}

func (mysqlDB *MysqlObj) Insert(sqlStr string) (ins_id int64, err error) {
	var result sql.Result
	result, err = mysqlDB.db.Exec(sqlStr)
	if err != nil {
		return 0, err
	}
	ins_id, err = result.LastInsertId();
	return
}

func (mysqlDB *MysqlObj) update(sqlStr string, args ...interface{}) (line int64, err error) {
	//db.Exec("update test set name = '000' where id > ?", 2);
	var result sql.Result
	result, err = mysqlDB.db.Exec(sqlStr, args...)

	if err != nil {
		return 0, err
	}

	line, err = result.RowsAffected()
	return
}

func (mysqlDB *MysqlObj) delete(sqlStr string, args ... interface{}) (line int64, err error) {
	var result sql.Result
	result, err = mysqlDB.db.Exec(sqlStr, args...)

	if err != nil {
		return 0, err
	}

	line, err = result.RowsAffected()
	return
}

//func mysql() {
//
//	//关闭数据库，db会被多个goroutine共享，可以不调用
//
//	//查询数据，指定字段名，返回sql.Rows结果集
//	rows, _ := db.Query("SELECT id,name FROM test");
//	id := 0;
//	name := "";
//	for rows.Next() {
//		rows.Scan(&id, &name);
//		fmt.Println(id, name);
//	}
//
//	//查询数据，取所有字段
//	rows2, _ := db.Query("SELECT * FROM test");
//	//返回所有列
//	cols, _ := rows2.Columns();
//	//这里表示一行所有列的值，用[]byte表示
//	vals := make([][]byte, len(cols));
//	//这里表示一行填充数据
//	scans := make([]interface{}, len(cols));
//	//这里scans引用vals，把数据填充到[]byte里
//	for k, _ := range vals {
//		scans[k] = &vals[k];
//	}
//
//	i := 0;
//	result := make(map[int]map[string]string);
//	for rows2.Next() {
//		//填充数据
//		rows2.Scan(scans...);
//		//每行数据
//		row := make(map[string]string);
//		//把vals中的数据复制到row中
//		for k, v := range vals {
//			key := cols[k];
//			//这里把[]byte数据转成string
//			row[key] = string(v);
//		}
//		//放入结果集
//		result[i] = row;
//		i++;
//	}
//	fmt.Println(result);
//
//	//查询一行数据
//	rows3 := db.QueryRow("SELECT id,name FROM test where id = ?", 1);
//	rows3.Scan(&id, &name);
//	fmt.Println(id, name);
//
//	//插入一行数据
//	ret, _ := db.Exec("insert into test(id,name) values(null, '444')");
//	//获取插入ID
//	ins_id, _ := ret.LastInsertId();
//	fmt.Println(ins_id);
//
//	//更新数据
//	ret2, _ := db.Exec("update test set name = '000' where id > ?", 2);
//	//获取影响行数
//	aff_nums, _ := ret2.RowsAffected();
//	fmt.Println(aff_nums);
//
//	//删除数据
//	ret3, _ := db.Exec("delete from test where id = ?", 3);
//	//获取影响行数
//	del_nums, _ := ret3.RowsAffected();
//	fmt.Println(del_nums);
//
//	//预处理语句
//	stmt, _ := db.Prepare("select id,name from test where id = ?");
//	rows4, _ := stmt.Query(3);
//	//注意这里需要Next()下，不然下面取不到值
//	rows4.Next();
//	rows4.Scan(&id, &name);
//	fmt.Println(id, name);
//	stmt2, _ := db.Prepare("insert into test values(null, ?, ?)");
//	rows5, _ := stmt2.Exec("666", 66);
//	fmt.Println(rows5.RowsAffected());
//
//	//事务处理
//	tx, _ := db.Begin();
//	ret4, _ := tx.Exec("update test set price = price + 100 where id = ?", 1);
//	ret5, _ := tx.Exec("update test set price = price - 100 where id = ?", 2);
//	upd_nums1, _ := ret4.RowsAffected();
//	upd_nums2, _ := ret5.RowsAffected();
//	if upd_nums1 > 0 && upd_nums2 > 0 {
//		//只有两条更新同时成功，那么才提交
//		tx.Commit();
//	} else {
//		//否则回滚
//		tx.Rollback();
//	}
//}
