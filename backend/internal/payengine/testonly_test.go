package payengine

import "context"

// 下面三个函数生产代码不用，只给测试用（审查 P1-12）：终态一律走 move(…, moveEnd / moveEndKeepSession)，
// 处理器列单据用 ListBy。

// finish 同 transition，另设 finished_at 并清空 session_sealed。
func finish(ctx context.Context, no, from, to, level, msg string, set map[string]any) error {
	return move(ctx, no, from, to, level, msg, set, moveEnd)
}

// listOrders 按 id 倒序分页列出单据；state 为空不筛选。返回总数。
func listOrders(ctx context.Context, state string, page, size int) ([]Order, int, error) {
	return listOrdersBy(ctx, OrderFilter{State: state}, page, size)
}

// List 按 id 倒序分页列出单据，state 为空不筛选；返回总数。
func (s *Service) List(ctx context.Context, state string, page, size int) ([]Order, int, error) {
	return listOrders(ctx, state, page, size)
}
