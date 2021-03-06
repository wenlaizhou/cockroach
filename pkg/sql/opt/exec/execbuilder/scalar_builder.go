// Copyright 2018 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License.

package execbuilder

import (
	"fmt"

	"github.com/cockroachdb/cockroach/pkg/sql/opt/opt"
	"github.com/cockroachdb/cockroach/pkg/sql/opt/xform"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
)

type buildScalarCtx struct {
	ivh tree.IndexedVarHelper

	// ivarMap is a map from opt.ColumnIndex to the index of an IndexedVar.
	// If a ColumnIndex is not in the map, it cannot appear in the expression.
	ivarMap opt.ColMap
}

type buildFunc func(b *Builder, ctx *buildScalarCtx, ev xform.ExprView) tree.TypedExpr

var scalarBuildFuncMap [opt.NumOperators]buildFunc

func init() {
	// This code is not inline to avoid an initialization loop error (some of
	// the functions depend on scalarBuildFuncMap which in turn depends on the
	// functions).
	scalarBuildFuncMap = [opt.NumOperators]buildFunc{
		opt.VariableOp:    (*Builder).buildVariable,
		opt.ConstOp:       (*Builder).buildTypedExpr,
		opt.PlaceholderOp: (*Builder).buildTypedExpr,
		opt.TupleOp:       (*Builder).buildTuple,
	}

	for _, op := range opt.BooleanOperators {
		scalarBuildFuncMap[op] = (*Builder).buildBoolean
	}

	for _, op := range opt.ComparisonOperators {
		scalarBuildFuncMap[op] = (*Builder).buildComparison
	}

	for _, op := range opt.BinaryOperators {
		scalarBuildFuncMap[op] = (*Builder).buildBinary
	}

	for _, op := range opt.UnaryOperators {
		scalarBuildFuncMap[op] = (*Builder).buildUnary
	}
}

// buildScalar converts a scalar expression to a TypedExpr. Variables are mapped
// according to ctx.
func (b *Builder) buildScalar(ctx *buildScalarCtx, ev xform.ExprView) tree.TypedExpr {
	if fn := scalarBuildFuncMap[ev.Operator()]; fn != nil {
		return fn(b, ctx, ev)
	}
	panic(fmt.Sprintf("unsupported op %s", ev.Operator()))
}

func (b *Builder) buildTypedExpr(ctx *buildScalarCtx, ev xform.ExprView) tree.TypedExpr {
	return ev.Private().(tree.TypedExpr)
}

func (b *Builder) buildVariable(ctx *buildScalarCtx, ev xform.ExprView) tree.TypedExpr {
	colIndex := ev.Private().(opt.ColumnIndex)
	idx, ok := ctx.ivarMap.Get(int(colIndex))
	if !ok {
		panic(fmt.Sprintf("cannot map variable %d to an indexed var", colIndex))
	}
	return ctx.ivh.IndexedVarWithType(idx, ev.Metadata().ColumnType(colIndex))
}

func (b *Builder) buildTuple(ctx *buildScalarCtx, ev xform.ExprView) tree.TypedExpr {
	if isTupleOfConstants(ev) {
		datums := make(tree.Datums, ev.ChildCount())
		for i := 0; i < ev.ChildCount(); i++ {
			child := ev.Child(i)
			datums[i] = *child.Private().(*tree.Datum)
		}
		return tree.NewDTuple(datums...)
	}

	typedExprs := make([]tree.TypedExpr, ev.ChildCount())
	for i := 0; i < ev.ChildCount(); i++ {
		typedExprs[i] = b.buildScalar(ctx, ev.Child(i))
	}
	return tree.NewTypedTuple(typedExprs)
}

func (b *Builder) buildBoolean(ctx *buildScalarCtx, ev xform.ExprView) tree.TypedExpr {
	switch ev.Operator() {
	case opt.AndOp, opt.OrOp:
		left := b.buildScalar(ctx, ev.Child(0))
		right := b.buildScalar(ctx, ev.Child(1))
		if ev.Operator() == opt.AndOp {
			return tree.NewTypedAndExpr(left, right)
		}
		return tree.NewTypedOrExpr(left, right)

	case opt.NotOp:
		return tree.NewTypedNotExpr(b.buildScalar(ctx, ev.Child(0)))

	case opt.TrueOp:
		return tree.DBoolTrue

	case opt.FalseOp:
		return tree.DBoolFalse

	default:
		panic(fmt.Sprintf("invalid op %s", ev.Operator()))
	}
}

func (b *Builder) buildComparison(ctx *buildScalarCtx, ev xform.ExprView) tree.TypedExpr {
	return tree.NewTypedComparisonExpr(
		opt.ComparisonOpReverseMap[ev.Operator()],
		b.buildScalar(ctx, ev.Child(0)),
		b.buildScalar(ctx, ev.Child(1)),
	)
}

func (b *Builder) buildUnary(ctx *buildScalarCtx, ev xform.ExprView) tree.TypedExpr {
	return tree.NewTypedUnaryExpr(
		opt.UnaryOpReverseMap[ev.Operator()],
		b.buildScalar(ctx, ev.Child(0)),
		ev.Logical().Scalar.Type,
	)
}

func (b *Builder) buildBinary(ctx *buildScalarCtx, ev xform.ExprView) tree.TypedExpr {
	return tree.NewTypedBinaryExpr(
		opt.BinaryOpReverseMap[ev.Operator()],
		b.buildScalar(ctx, ev.Child(0)),
		b.buildScalar(ctx, ev.Child(1)),
		ev.Logical().Scalar.Type,
	)
}

func isTupleOfConstants(ev xform.ExprView) bool {
	if ev.Operator() != opt.TupleOp {
		return false
	}
	for i := 0; i < ev.ChildCount(); i++ {
		child := ev.Child(i)
		if child.Operator() != opt.ConstOp {
			return false
		}
	}
	return true
}
