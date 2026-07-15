package federation

type Row map[string]any
type Joined struct{ Left, Right Row }

// HashJoin 以内存哈希表执行等值连接，并用 maxOutput 限制结果膨胀。
func HashJoin(left, right []Row, leftKey, rightKey string, maxOutput int) ([]Joined, error) {
	if maxOutput <= 0 {
		maxOutput = 100000
	}
	index := make(map[any][]Row, len(right))
	for _, row := range right {
		if key := row[rightKey]; key != nil {
			index[key] = append(index[key], row)
		}
	}
	out := make([]Joined, 0)
	for _, l := range left {
		for _, r := range index[l[leftKey]] {
			if len(out) >= maxOutput {
				return nil, ErrOutputLimit
			}
			out = append(out, Joined{Left: l, Right: r})
		}
	}
	return out, nil
}

type limitError string

// Error 实现连接结果超限错误的文本表示。
func (e limitError) Error() string { return string(e) }

const ErrOutputLimit limitError = "federated join output limit exceeded"
