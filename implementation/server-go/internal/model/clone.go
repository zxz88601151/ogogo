package model

// clone 深拷贝内容源，避免锁外引用内部切片。
func (s *Source) clone() Source {
	out := Source{
		Meta: s.Meta,
	}
	if s.Items != nil {
		out.Items = make([]ContentItem, len(s.Items))
		copy(out.Items, s.Items)
	}
	if s.Tombstones != nil {
		out.Tombstones = make([]Tombstone, len(s.Tombstones))
		copy(out.Tombstones, s.Tombstones)
	}
	return out
}

// Clone 导出深拷贝（供 store 使用）。
func (s *Source) Clone() Source { return s.clone() }