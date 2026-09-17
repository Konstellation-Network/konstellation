package precompile_test

import (
	"github.com/Konstellation-Network/konstellation/x/compliance/keeper"
	"github.com/Konstellation-Network/konstellation/x/compliance/precompile"
)

var _ precompile.Keeper = keeper.Keeper{}
