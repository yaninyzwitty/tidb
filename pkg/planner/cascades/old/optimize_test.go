// Copyright 2018 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package old

import (
	"context"
	"math"
	"testing"

	"github.com/pingcap/tidb/pkg/domain"
	"github.com/pingcap/tidb/pkg/expression"
	"github.com/pingcap/tidb/pkg/infoschema"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/planner/cascades/pattern"
	plannercore "github.com/pingcap/tidb/pkg/planner/core"
	"github.com/pingcap/tidb/pkg/planner/core/base"
	"github.com/pingcap/tidb/pkg/planner/core/operator/logicalop"
	"github.com/pingcap/tidb/pkg/planner/core/resolve"
	"github.com/pingcap/tidb/pkg/planner/memo"
	"github.com/pingcap/tidb/pkg/planner/property"
	"github.com/stretchr/testify/require"
)

func TestImplGroupZeroCost(t *testing.T) {
	p := parser.New()
	ctx := plannercore.MockContext()
	defer func() {
		domain.GetDomain(ctx).StatsHandle().Close()
	}()
	is := infoschema.MockInfoSchema([]*model.TableInfo{plannercore.MockSignedTable()})
	domain.GetDomain(ctx).MockInfoCacheAndLoadInfoSchema(is)

	stmt, err := p.ParseOneStmt("select t1.a, t2.a from t as t1 left join t as t2 on t1.a = t2.a where t1.a < 1.0", "", "")
	require.NoError(t, err)
	nodeW := resolve.NewNodeW(stmt)
	plan, err := plannercore.BuildLogicalPlanForTest(context.Background(), ctx, nodeW, is)
	require.NoError(t, err)

	logic, ok := plan.(base.LogicalPlan)
	require.True(t, ok)

	rootGroup := memo.Convert2Group(logic)
	prop := &property.PhysicalProperty{
		ExpectedCnt: math.MaxFloat64,
	}
	impl, err := NewOptimizer().implGroup(rootGroup, prop, 0.0)
	require.NoError(t, err)
	require.Nil(t, impl)
}

func TestInitGroupSchema(t *testing.T) {
	p := parser.New()
	ctx := plannercore.MockContext()
	defer func() {
		domain.GetDomain(ctx).StatsHandle().Close()
	}()
	is := infoschema.MockInfoSchema([]*model.TableInfo{plannercore.MockSignedTable()})
	domain.GetDomain(ctx).MockInfoCacheAndLoadInfoSchema(is)

	stmt, err := p.ParseOneStmt("select a from t", "", "")
	require.NoError(t, err)

	nodeW := resolve.NewNodeW(stmt)
	plan, err := plannercore.BuildLogicalPlanForTest(context.Background(), ctx, nodeW, is)
	require.NoError(t, err)

	logic, ok := plan.(base.LogicalPlan)
	require.True(t, ok)

	g := memo.Convert2Group(logic)
	require.NotNil(t, g)
	require.NotNil(t, g.Prop)
	require.Equal(t, 1, g.Prop.Schema.Len())
	require.Nil(t, g.Prop.Stats)
}

func TestFillGroupStats(t *testing.T) {
	p := parser.New()
	ctx := plannercore.MockContext()
	defer func() {
		domain.GetDomain(ctx).StatsHandle().Close()
	}()
	is := infoschema.MockInfoSchema([]*model.TableInfo{plannercore.MockSignedTable()})
	domain.GetDomain(ctx).MockInfoCacheAndLoadInfoSchema(is)

	stmt, err := p.ParseOneStmt("select * from t t1 join t t2 on t1.a = t2.a", "", "")
	require.NoError(t, err)

	nodeW := resolve.NewNodeW(stmt)
	plan, err := plannercore.BuildLogicalPlanForTest(context.Background(), ctx, nodeW, is)
	require.NoError(t, err)

	logic, ok := plan.(base.LogicalPlan)
	require.True(t, ok)

	rootGroup := memo.Convert2Group(logic)
	err = NewOptimizer().fillGroupStats(rootGroup)
	require.NoError(t, err)
	require.NotNil(t, rootGroup.Prop.Stats)
}

func TestPreparePossibleProperties(t *testing.T) {
	p := parser.New()
	ctx := plannercore.MockContext()
	defer func() {
		domain.GetDomain(ctx).StatsHandle().Close()
	}()
	is := infoschema.MockInfoSchema([]*model.TableInfo{plannercore.MockSignedTable()})
	domain.GetDomain(ctx).MockInfoCacheAndLoadInfoSchema(is)
	optimizer := NewOptimizer()

	optimizer.ResetTransformationRules(map[pattern.Operand][]Transformation{
		pattern.OperandDataSource: {
			NewRuleEnumeratePaths(),
		},
	})
	defer func() {
		optimizer.ResetTransformationRules(DefaultRuleBatches...)
	}()

	stmt, err := p.ParseOneStmt("select f, sum(a) from t group by f", "", "")
	require.NoError(t, err)

	nodeW := resolve.NewNodeW(stmt)
	plan, err := plannercore.BuildLogicalPlanForTest(context.Background(), ctx, nodeW, is)
	require.NoError(t, err)

	logic, ok := plan.(base.LogicalPlan)
	require.True(t, ok)

	logic, err = optimizer.onPhasePreprocessing(ctx.GetPlanCtx(), logic)
	require.NoError(t, err)

	// collect the target columns: f, a
	ds, ok := logic.Children()[0].Children()[0].(*logicalop.DataSource)
	require.True(t, ok)

	var columnF, columnA *expression.Column
	for i, col := range ds.Columns {
		if col.Name.L == "f" {
			columnF = ds.Schema().Columns[i]
		} else if col.Name.L == "a" {
			columnA = ds.Schema().Columns[i]
		}
	}
	require.NotNil(t, columnF)
	require.NotNil(t, columnA)

	agg, ok := logic.Children()[0].(*logicalop.LogicalAggregation)
	require.True(t, ok)

	group := memo.Convert2Group(agg)
	require.NoError(t, optimizer.onPhaseExploration(ctx.GetPlanCtx(), group))

	// The memo looks like this:
	// Group#0 Schema:[Column#13,test.t.f]
	//   Aggregation_2 input:[Group#1], group by:test.t.f, funcs:sum(test.t.a), firstrow(test.t.f)
	// Group#1 Schema:[test.t.a,test.t.f]
	//   TiKVSingleGather_5 input:[Group#2], table:t
	//   TiKVSingleGather_9 input:[Group#3], table:t, index:f_g
	//   TiKVSingleGather_7 input:[Group#4], table:t, index:f
	// Group#2 Schema:[test.t.a,test.t.f]
	//   TableScan_4 table:t, pk col:test.t.a
	// Group#3 Schema:[test.t.a,test.t.f]
	//   IndexScan_8 table:t, index:f, g
	// Group#4 Schema:[test.t.a,test.t.f]
	//   IndexScan_6 table:t, index:f
	propMap := make(map[*memo.Group][][]*expression.Column)
	aggProp := preparePossibleProperties(group, propMap)
	// We only have one prop for Group0 : f
	require.Len(t, aggProp, 1)
	require.True(t, aggProp[0][0].EqualColumn(columnF))

	gatherGroup := group.Equivalents.Front().Value.(*memo.GroupExpr).Children[0]
	gatherProp, ok := propMap[gatherGroup]
	require.True(t, ok)
	// We have 2 props for Group1: [f], [a]
	require.Len(t, gatherProp, 2)
	for _, prop := range gatherProp {
		require.Len(t, prop, 1)
		require.True(t, prop[0].EqualColumn(columnA) || prop[0].EqualColumn(columnF))
	}
}

// fakeTransformation is used for TestAppliedRuleSet.
type fakeTransformation struct {
	baseRule
	appliedTimes int
}

// OnTransform implements Transformation interface.
func (rule *fakeTransformation) OnTransform(old *memo.ExprIter) (newExprs []*memo.GroupExpr, eraseOld bool, eraseAll bool, err error) {
	rule.appliedTimes++
	old.GetExpr().AddAppliedRule(rule)
	return []*memo.GroupExpr{old.GetExpr()}, true, false, nil
}

func TestAppliedRuleSet(t *testing.T) {
	p := parser.New()
	ctx := plannercore.MockContext()
	defer func() {
		domain.GetDomain(ctx).StatsHandle().Close()
	}()
	is := infoschema.MockInfoSchema([]*model.TableInfo{plannercore.MockSignedTable()})
	domain.GetDomain(ctx).MockInfoCacheAndLoadInfoSchema(is)
	optimizer := NewOptimizer()

	rule := fakeTransformation{}
	rule.pattern = pattern.NewPattern(pattern.OperandProjection, pattern.EngineAll)
	optimizer.ResetTransformationRules(map[pattern.Operand][]Transformation{
		pattern.OperandProjection: {
			&rule,
		},
	})
	defer func() {
		optimizer.ResetTransformationRules(DefaultRuleBatches...)
	}()

	stmt, err := p.ParseOneStmt("select 1", "", "")
	require.NoError(t, err)

	nodeW := resolve.NewNodeW(stmt)
	plan, err := plannercore.BuildLogicalPlanForTest(context.Background(), ctx, nodeW, is)
	require.NoError(t, err)

	logic, ok := plan.(base.LogicalPlan)
	require.True(t, ok)

	group := memo.Convert2Group(logic)
	require.NoError(t, optimizer.onPhaseExploration(ctx.GetPlanCtx(), group))
	require.Equal(t, 1, rule.appliedTimes)
}
