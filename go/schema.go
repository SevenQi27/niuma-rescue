package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type BaseField struct {
	FieldID   string         `json:"field_id"`
	FieldName string         `json:"field_name"`
	Type      int            `json:"type"`
	Property  map[string]any `json:"property"`
}

func (f *Feishu) fieldBase() string {
	return "/open-apis/bitable/v1/apps/" + f.baseToken + "/tables/" + f.tableID + "/fields"
}

func (f *Feishu) listFields() ([]BaseField, error) {
	data, err := f.api("GET", f.fieldBase()+"?page_size=100", nil)
	if err != nil {
		return nil, err
	}
	var d struct {
		Items []BaseField `json:"items"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	return d.Items, nil
}

func (f *Feishu) createSingleSelect(name string, options []map[string]any) error {
	_, err := f.api("POST", f.fieldBase(), map[string]any{
		"field_name": name, "type": 3, "property": map[string]any{"options": options},
	})
	return err
}

func (f *Feishu) updateSingleSelect(field BaseField, property map[string]any) error {
	_, err := f.api("PUT", f.fieldBase()+"/"+field.FieldID, map[string]any{
		"field_name": field.FieldName, "type": field.Type, "property": property,
	})
	return err
}

func fieldByName(fields []BaseField, name string) *BaseField {
	for i := range fields {
		if fields[i].FieldName == name {
			return &fields[i]
		}
	}
	return nil
}

func selectOptions(field *BaseField) []any {
	if field == nil || field.Property == nil {
		return nil
	}
	items, _ := field.Property["options"].([]any)
	return items
}

func hasOption(field *BaseField, name string) bool {
	for _, item := range selectOptions(field) {
		if option, ok := item.(map[string]any); ok && fieldText(option["name"]) == name {
			return true
		}
	}
	return false
}

func ensureOption(fs *Feishu, field *BaseField, name string, color int) (bool, error) {
	if field == nil {
		return false, errf("字段不存在")
	}
	if field.Type != 3 {
		return false, errf("字段 %s 必须是单选，当前 type=%d", field.FieldName, field.Type)
	}
	if hasOption(field, name) {
		return false, nil
	}
	property := map[string]any{}
	for k, v := range field.Property {
		property[k] = v
	}
	options := append([]any{}, selectOptions(field)...)
	options = append(options, map[string]any{"name": name, "color": color})
	property["options"] = options
	if err := fs.updateSingleSelect(*field, property); err != nil {
		return false, err
	}
	return true, nil
}

func runSchemaCommand(ensure bool) int {
	fs := newFeishu(cfg)
	fields, err := fs.listFields()
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取 Base 字段失败:", err)
		return 1
	}
	taskType := fieldByName(fields, FTaskType)
	status := fieldByName(fields, FStatus)
	fmt.Printf("%s: %v\n", FTaskType, taskType != nil)
	fmt.Printf("%s/%s: %v\n", FStatus, SBug, hasOption(status, SBug))
	if !ensure {
		if taskType == nil || !hasOption(taskType, TaskRequirement) || !hasOption(taskType, TaskBug) || !hasOption(status, SBug) || !hasOption(status, SCodeWait) {
			return 2
		}
		return 0
	}
	if status == nil {
		fmt.Fprintln(os.Stderr, "Base 缺少状态字段，拒绝继续")
		return 1
	}
	if taskType == nil {
		if err := fs.createSingleSelect(FTaskType, []map[string]any{
			{"name": TaskRequirement, "color": 0}, {"name": TaskBug, "color": 1},
		}); err != nil {
			fmt.Fprintln(os.Stderr, "创建任务类型字段失败:", err)
			return 1
		}
		fmt.Println("created:", FTaskType)
	} else {
		for i, name := range []string{TaskRequirement, TaskBug} {
			changed, err := ensureOption(fs, taskType, name, i)
			if err != nil {
				fmt.Fprintln(os.Stderr, "更新任务类型失败:", err)
				return 1
			}
			if changed {
				fmt.Println("added option:", FTaskType+"/"+name)
				fields, _ = fs.listFields()
				taskType = fieldByName(fields, FTaskType)
			}
		}
	}
	for _, option := range []struct {
		name  string
		color int
	}{{SBug, 4}, {SCodeWait, 2}} {
		changed, err := ensureOption(fs, status, option.name, option.color)
		if err != nil {
			fmt.Fprintln(os.Stderr, "更新状态字段失败:", err)
			return 1
		}
		if changed {
			fmt.Println("added option:", FStatus+"/"+option.name)
			fields, _ = fs.listFields()
			status = fieldByName(fields, FStatus)
		}
	}
	fmt.Println("Bug schema ready")
	return 0
}
