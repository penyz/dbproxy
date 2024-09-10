package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/ecodeclub/ekit/syncx"
	"github.com/meoying/dbproxy/internal/datasource"
	"github.com/meoying/dbproxy/internal/datasource/transaction"
	"github.com/meoying/dbproxy/internal/protocol/mysql/internal/pcontext"
	"github.com/meoying/dbproxy/internal/protocol/mysql/plugin"
	"github.com/meoying/dbproxy/internal/query"
)

type baseHandler struct {
	ds datasource.DataSource
	// connID2Tx 在复合操作中的并发安全性，依赖于 Conn 中不可能出现并发Tx.
	// 即一个 Conn 不会也不可能同时存在两个 Tx
	connID2Tx         syncx.Map[uint32, *transaction.TxDatasource]
	stmtID2Stmt       syncx.Map[uint32, datasource.Stmt]
	stmtID2PrepareCtx syncx.Map[uint32, *pcontext.Context]
	newTxCtx          func(ctx context.Context) context.Context
}

func newBaseHandler(ds datasource.DataSource, txType string) *baseHandler {
	return &baseHandler{
		ds: ds,
		newTxCtx: func(ctx context.Context) context.Context {
			return transaction.UsingTxType(ctx, txType)
		},
	}
}

// getDatasource 获取本次执行需要使用的数据源
func (h *baseHandler) getDatasource(ctx *pcontext.Context) datasource.DataSource {
	if tx := h.getTxByConnID(ctx.ConnID); tx != nil {
		return tx
	}
	return h.ds
}

// getTxByConnID 根据客户端连接ID获取事务对象, 因为事务是与链接绑定的
func (h *baseHandler) getTxByConnID(connID uint32) *transaction.TxDatasource {
	if tx, ok := h.connID2Tx.Load(connID); ok {
		return tx
	}
	return nil
}

// isInTransaction 通过Conn ID判断其是否处于事务状态中
func (h *baseHandler) isInTransaction(connID uint32) bool {
	// 有connID对应的Tx即表示对应的conn处于事务状态中
	_, ok := h.connID2Tx.Load(connID)
	return ok
}

// handleStartTransactionStmt 处理开启事务语句
func (h *baseHandler) handleStartTransactionStmt(ctx *pcontext.Context) (*plugin.Result, error) {
	tx, err := h.ds.BeginTx(h.newTxCtx(ctx), nil)
	if err != nil {
		return nil, err
	}
	h.connID2Tx.Store(ctx.ConnID, transaction.NewTransactionDataSource(tx))
	return &plugin.Result{InTransactionState: true}, nil
}

// handleCommitStmt 处理提交事务语句
func (h *baseHandler) handleCommitStmt(ctx *pcontext.Context) (*plugin.Result, error) {
	var err error
	tx := h.getTxByConnID(ctx.ConnID)
	if tx != nil {
		err = tx.Commit()
	}
	if err == nil {
		h.connID2Tx.Delete(ctx.ConnID)
	}
	return &plugin.Result{}, err
}

// handleRollbackStmt 处理回滚事务语句
func (h *baseHandler) handleRollbackStmt(ctx *pcontext.Context) (*plugin.Result, error) {
	var err error
	tx := h.getTxByConnID(ctx.ConnID)
	if tx != nil {
		err = tx.Rollback()
	}
	if err == nil {
		h.connID2Tx.Delete(ctx.ConnID)
	}
	return &plugin.Result{}, err
}

func (h *baseHandler) getStmtPreparer(ctx *pcontext.Context) datasource.StmtPreparer {
	if tx := h.getTxByConnID(ctx.ConnID); tx != nil {
		return tx
	}
	return h.ds
}

func (h *baseHandler) handlePrepareStmt(ctx *pcontext.Context, query query.Query) (*plugin.Result, error) {
	stmt, err := h.getStmtPreparer(ctx).Prepare(ctx, query)
	if err != nil {
		return nil, err
	}
	h.stmtID2Stmt.Store(ctx.StmtID, stmt)
	h.stmtID2PrepareCtx.Store(ctx.StmtID, &pcontext.Context{
		Context: ctx.Context,
		// SELECT * FROM order where `user_id` = ?;
		// SELECT * FROM order where `user_id` = '?';
		ParsedQuery: pcontext.NewParsedQuery(h.convertQuery(ctx.Query), nil),
		Query:       ctx.Query,
		ConnID:      ctx.ConnID,
		StmtID:      ctx.StmtID,
	})
	return &plugin.Result{
		InTransactionState: h.isInTransaction(ctx.ConnID),
		StmtID:             ctx.StmtID,
	}, nil
}

func (h *baseHandler) getStmtByStmtID(stmtID uint32) (datasource.Stmt, error) {
	if stmt, ok := h.stmtID2Stmt.Load(stmtID); ok {
		return stmt, nil
	}
	return nil, fmt.Errorf("未找到id为%d的stmt", stmtID)
}

func (h *baseHandler) getPrepareContextByStmtID(stmtID uint32) (*pcontext.Context, error) {
	if ctx, ok := h.stmtID2PrepareCtx.Load(stmtID); ok {
		return ctx, nil
	}
	return nil, fmt.Errorf("未找到id为%d的pcontext.Context", stmtID)
}

func (h *baseHandler) handleDeallocatePrepareStmt(ctx *pcontext.Context) (*plugin.Result, error) {
	stmt, err := h.getStmtByStmtID(ctx.StmtID)
	if err != nil {
		return nil, err
	}
	err = stmt.Close()
	h.stmtID2Stmt.Delete(ctx.StmtID)
	h.stmtID2PrepareCtx.Delete(ctx.StmtID)
	return &plugin.Result{
		InTransactionState: h.isInTransaction(ctx.ConnID),
	}, err
}

func (h *baseHandler) convertQuery(query string) string {
	return strings.ReplaceAll(query, "?", "'?'")
}
