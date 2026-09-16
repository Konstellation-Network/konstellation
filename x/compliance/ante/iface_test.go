package ante_test

import (
	"github.com/Konstellation-Network/konstellation/x/compliance/ante"
	"github.com/Konstellation-Network/konstellation/x/compliance/keeper"
)

var _ ante.FreezeChecker = keeper.Keeper{}
