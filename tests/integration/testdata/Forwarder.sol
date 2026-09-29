// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

/// @title Forwarder — the shape of any contract that moves value on
/// someone's behalf (a router, a smart wallet, a payout contract). Used by
/// konstellation/tests/integration to reach the paths the ante handler
/// cannot see: an internal CALL with value and an internal precompile call.
contract Forwarder {
    receive() external payable {}

    /// Native value out of this contract's own balance.
    function send(address payable to, uint256 amount) external {
        (bool ok, ) = to.call{value: amount}("");
        require(ok, "send failed");
    }

    /// Any call, forwarding msg.value; bubbles the callee's revert data.
    function call(address target, bytes calldata data) external payable returns (bytes memory) {
        (bool ok, bytes memory ret) = target.call{value: msg.value}(data);
        if (!ok) {
            assembly {
                revert(add(ret, 32), mload(ret))
            }
        }
        return ret;
    }
}
