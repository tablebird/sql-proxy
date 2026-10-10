package backend

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSqlSubmitResponse(t *testing.T) {
	data := "{\n" +
		"\t\"id\": 4588,\n" +
		"\t\"workflow_id\": 4588,\n" +
		"\t\"workflow\": {\"id\":4588,\"workflow_name\":\"test\",\"demand_url\":\"\",\"group_id\":1,\"group_name\":\"测试环境\",\"db_name\":\"test\",\"syntax_type\":2,\"is_backup\":true,\"engineer\":\"name\",\"engineer_display\":\"name\",\"status\":\"workflow_manreviewing\",\"audit_auth_groups\":\"6,3\",\"run_date_start\":null,\"run_date_end\":null,\"create_time\":\"2026-09-10T15:13:53.944015\",\"finish_time\":null,\"is_manual\":0,\"instance\":1},\n" +
		"\t\"sql_content\": \"UPDATE test SET status=1 WHERE id='1'\",\n" +
		"\t\"review_content\": \"[{\\\"id\\\": 1, \\\"stage\\\": \\\"CHECKED\\\", \\\"errlevel\\\": 0, \\\"stagestatus\\\": \\\"Audit Completed\\\", \\\"errormessage\\\": \\\"\\\", \\\"sql\\\": \\\"use `test`\\\", \\\"affected_rows\\\": 0, \\\"sequence\\\": \\\"0_0_00000000\\\", \\\"backup_dbname\\\": \\\"\\\", \\\"execute_time\\\": \\\"0\\\", \\\"sqlsha1\\\": \\\"\\\", \\\"backup_time\\\": \\\"0\\\", \\\"actual_affected_rows\\\": \\\"\\\"}, {\\\"id\\\": 2, \\\"stage\\\": \\\"CHECKED\\\", \\\"errlevel\\\": 0, \\\"stagestatus\\\": \\\"Audit Completed\\\", \\\"errormessage\\\": \\\"\\\", \\\"sql\\\": \\\"UPDATE conference_meeting_submission SET status=1 WHERE id='1'\\\", \\\"affected_rows\\\": 1, \\\"sequence\\\": \\\"0_0_00000001\\\", \\\"backup_dbname\\\": \\\"\\\", \\\"execute_time\\\": \\\"0\\\", \\\"sqlsha1\\\": \\\"\\\", \\\"backup_time\\\": \\\"0\\\", \\\"actual_affected_rows\\\": \\\"\\\"}]\",\n" +
		"\t\"execute_result\": \"\"\n" +
		"}"
	var resp sqlSubmitResponse
	err := json.Unmarshal([]byte(data), &resp)
	require.NoError(t, err)
	require.Equal(t, `{"id":4588,"workflow_name":"test","demand_url":"","group_id":1,"group_name":"测试环境","db_name":"test","syntax_type":2,"is_backup":true,"engineer":"name","engineer_display":"name","status":"workflow_manreviewing","audit_auth_groups":"6,3","run_date_start":null,"run_date_end":null,"create_time":"2026-09-10T15:13:53.944015","finish_time":null,"is_manual":0,"instance":1}`, string(resp.Workflow))
}
